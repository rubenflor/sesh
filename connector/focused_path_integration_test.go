//go:build unix

package connector

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

const focusedPathIdentityFormat = "#{session_id}::" + bridgeServerFormat

type focusedPathDisplay struct {
	args   []string
	output string
	err    error
}

// Use native target resolution, including default-current fallback, without
// panePrivateShell.Cmd's injected current-session target. Hooks only set timing.
type focusedPathPrivateShell struct {
	*panePrivateShell
	afterIdentity, afterOwnership func()
	displays                      []focusedPathDisplay
	ownership                     [][]string
	events                        []string
	clientReads                   int
	clients                       []string
}

func (s *focusedPathPrivateShell) Cmd(_ string, args ...string) (string, error) {
	s.events = append(s.events, "display started")
	out, err := s.run(args...)
	s.displays = append(s.displays, focusedPathDisplay{append([]string(nil), args...), out, err})
	s.events = append(s.events, "display completed")
	if err == nil && s.afterIdentity != nil {
		after := s.afterIdentity
		s.afterIdentity = nil
		after()
	}
	return out, err
}

func (s *focusedPathPrivateShell) CmdCapture(_ string, args ...string) (string, error) {
	s.ownership = append(s.ownership, append([]string(nil), args...))
	out, err := s.run(args...)
	if err == nil && s.afterOwnership != nil {
		after := s.afterOwnership
		s.afterOwnership = nil
		after()
	}
	return out, err
}

func (s *focusedPathPrivateShell) ListCmd(_ string, args ...string) ([]string, error) {
	s.clientReads++
	if s.clients != nil {
		return s.clients, nil
	}
	return nil, fmt.Errorf("calling-pane resolution must not list clients: %v", args)
}

func TestFocusedSessionPathOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	// Match ticket 34's canonical /tmp parent so recorded socket identities
	// also agree on macOS, where /tmp is a symlink.
	parent, err := os.MkdirTemp("/tmp", "sesh-focus-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := os.Remove(parent); err != nil {
			t.Errorf("retaining private tmux parent %s: %v", parent, err)
		}
	})
	canonicalParent, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	t.Setenv("TMPDIR", canonicalParent)
	for _, mode := range []string{
		"calling pane exists", "literal ID name", "unmarked bridge-like name", "marked bridge caller",
		"stale caller", "stale bridge caller", "disappeared after identity", "disappeared after ownership",
		"outside literal ID name", "outside unmarked bridge-like name", "outside renamed bridge", "outside marked ID name",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture, native := newBridgePrivateFixture(t, binary)
			require.NoError(t, fixture.startAnchor())
			run := func(args ...string) string {
				t.Helper()
				out, err := native.run(args...)
				require.NoError(t, err)
				return out
			}
			state := func(id string) []string {
				return []string{
					run("display-message", "-p", "-t", id+":", "#{session_id} #{session_name} #{session_path} #{window_id} #{pane_id}"),
					run("show-environment", "-t", id),
					run("capture-pane", "-p", "-t", id+":"),
				}
			}
			pathA, pathB := filepath.Join(fixture.dir, "caller"), filepath.Join(fixture.dir, "unrelated")
			for _, path := range []string{pathA, pathB} {
				require.NoError(t, os.Mkdir(path, 0700))
			}
			outside := strings.HasPrefix(mode, "outside ")
			kind := strings.TrimPrefix(mode, "outside ")
			name := "caller"
			if kind == "literal ID name" || kind == "marked ID name" {
				name = "$0"
			} else if kind == "unmarked bridge-like name" {
				name = "sesh-ssh-user"
			} else if kind == "renamed bridge" {
				name = "renamed-bridge"
			}
			run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/anchor")
			require.Equal(t, "$1::%1", run("new-session", "-d", "-P", "-F", "#{session_id}::#{pane_id}", "-s", name, "-c", pathA, "--", "exec sleep 120"))
			require.Equal(t, "$2", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "spare", "--", "exec sleep 120"))
			run("kill-session", "-t", "$2:")
			require.Equal(t, "$3::%3", run("new-session", "-d", "-P", "-F", "#{session_id}::#{pane_id}", "-s", "unrelated", "-c", pathB, "--", "exec sleep 120"))
			// Leave the caller's session alive if just its original pane vanishes.
			require.Equal(t, "%4", run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "%1", "--", "exec sleep 120"))
			run("select-pane", "-t", "%4")
			marker := ""
			if kind == "marked bridge caller" || kind == "stale bridge caller" || kind == "renamed bridge" || kind == "marked ID name" {
				marker = "ssh/fixture/caller"
			}
			run("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, marker)
			if kind == "unmarked bridge-like name" {
				run("set-environment", "-u", "-t", "$1", model.SSHBridgeIdentityEnv)
				run("set-environment", "-g", model.SSHBridgeIdentityEnv, "ssh/global/ignored")
			}
			anchorBefore, unrelatedBefore := state("$0"), state("$3")
			t.Setenv("TMUX", native.socket+","+strconv.Itoa(fixture.starts[0].PID)+",1")
			t.Setenv("TMUX_PANE", "%1")
			t.Setenv("SESH_CLIENT", "")
			sh := &focusedPathPrivateShell{panePrivateShell: native}
			target := "%1"
			if outside {
				// Preserve the existing native outside-tmux ownership cases with
				// an explicitly selected client among unrelated attached clients.
				t.Setenv("TMUX", "")
				t.Setenv("SESH_CLIENT", "chosen")
				sh.clients = []string{"other::/dev/tty1::$3::999", "chosen::/dev/tty2::$1::100"}
				target = "$1:"
			}
			removals := 0
			removePane := func() {
				require.Len(t, sh.displays, 1, "identity must complete before caller removal")
				require.NoError(t, sh.displays[0].err, "identity must succeed before caller removal")
				run("kill-pane", "-t", target)
				require.NotContains(t, strings.Fields(run("list-panes", "-a", "-F", "#{pane_id}")), target)
				removals++
				sh.events = append(sh.events, "caller pane removed")
			}
			if mode == "disappeared after identity" {
				sh.afterIdentity = removePane
			} else if mode == "disappeared after ownership" {
				sh.afterOwnership = removePane
			} else if strings.HasPrefix(mode, "stale") {
				run("kill-session", "-t", "$1:")
				run("kill-session", "-t", "$0:")
				// Prove the original unsafe query has a real unrelated candidate.
				require.Equal(t, "$3::%3::"+pathB, run("display-message", "-p", "#{session_id}::#{pane_id}::#{session_path}"))
			}

			path, err := tmux.NewTmux(oswrap.NewOs(), sh, "private-tmux").FocusedSessionPath()

			if strings.HasPrefix(mode, "disappeared") {
				require.Equal(t, 1, removals, "caller removal hook must run exactly once")
				require.Nil(t, sh.afterIdentity, "identity hook must be consumed")
				require.Nil(t, sh.afterOwnership, "ownership hook must be consumed")
				require.Len(t, sh.displays, 2, "identity and path queries must both run")
				identity, pathQuery := sh.displays[0], sh.displays[1]
				require.Equal(t, []string{"display-message", "-p", "-t", target, focusedPathIdentityFormat}, identity.args)
				require.NoError(t, identity.err)
				require.Equal(t, "$1::"+fixture.starts[0].Generation, identity.output)
				require.Equal(t, []string{"display-message", "-p", "-t", target, focusedPathIdentityFormat + "::#{session_path}"}, pathQuery.args)
				if pathQuery.err != nil {
					require.ErrorContains(t, pathQuery.err, "can't find pane: "+target)
					var exitErr *exec.ExitError
					require.ErrorAs(t, pathQuery.err, &exitErr)
					require.Equal(t, 1, exitErr.ExitCode())
					require.ErrorIs(t, err, pathQuery.err, "failure must come from the missing-pane path query")
				} else {
					// tmux can report a missing pane by leaving target metadata empty
					// while still expanding server formats and returning status zero.
					require.Equal(t, "::"+fixture.starts[0].Generation+"::", pathQuery.output)
					require.EqualError(t, err, "focused tmux session identity changed")
				}
				require.Equal(t, []string{
					"display started", "display completed", "caller pane removed",
					"display started", "display completed",
				}, sh.events)
			}

			if strings.HasPrefix(mode, "stale") || strings.HasPrefix(mode, "disappeared") || marker != "" {
				require.Error(t, err, "missing/marked caller returned path %q (unrelated path %q)", path, pathB)
				require.Empty(t, path)
				if marker != "" && !strings.HasPrefix(mode, "stale") {
					require.ErrorContains(t, err, "focus a local session")
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, pathA, path)
			}
			require.NotEqual(t, pathB, path)
			if outside {
				require.Equal(t, 1, sh.clientReads)
			} else {
				require.Zero(t, sh.clientReads)
			}
			require.NotEmpty(t, sh.displays)
			for _, display := range sh.displays {
				args := display.args
				require.GreaterOrEqual(t, len(args), 5, "no untargeted display-message fallback")
				require.Equal(t, []string{"display-message", "-p", "-t", target}, args[:4])
			}
			for _, args := range sh.ownership {
				require.Equal(t, []string{"show-environment", "-t", "$1"}, args[:3])
			}
			require.Equal(t, unrelatedBefore, state("$3"), "the surviving unrelated session is untouched")
			if !strings.HasPrefix(mode, "stale") {
				require.Equal(t, anchorBefore, state("$0"), "the marked anchor is untouched")
			}
			require.NoError(t, fixture.cleanup())
			require.NoDirExists(t, fixture.dir)
		})
	}
}
