//go:build unix

package connector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/git"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/pathwrap"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

// Inject only attached-client context and race timing. ID, generation,
// ownership, names and paths are resolved by the ticket-33 private server.
type currentSessionPrivateShell struct {
	*panePrivateShell
	afterInventory, afterCurrent, afterOwnership func()
	currentObserved                              bool
}

func (s *currentSessionPrivateShell) Cmd(bin string, args ...string) (string, error) {
	out, err := s.panePrivateShell.Cmd(bin, args...)
	if args[0] == "display-message" && len(args) == 3 {
		s.currentObserved = true
		if s.afterCurrent != nil {
			after := s.afterCurrent
			s.afterCurrent = nil
			after()
		}
	}
	return out, err
}

func (s *currentSessionPrivateShell) CmdCapture(bin string, args ...string) (string, error) {
	out, err := s.panePrivateShell.CmdCapture(bin, args...)
	if err == nil && args[0] == "show-environment" && s.currentObserved && s.afterOwnership != nil {
		after := s.afterOwnership
		s.afterOwnership = nil
		after()
	}
	return out, err
}

func (s *currentSessionPrivateShell) CmdCaptureRaw(ctx context.Context, bin string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
	inventory := args[0] == "-u" && args[1] == "list-sessions"
	if inventory {
		args = append([]string(nil), args...)
		// Inject attached-client state in the native expansion, before encoding.
		args[3] = strings.ReplaceAll(args[3], "#{session_attached}", "#{?#{==:#{session_id},$0},#{session_attached},1}")
	}
	result, err := s.panePrivateShell.CmdCaptureRaw(ctx, bin, stdoutLimit, stderrLimit, args...)
	if err == nil && inventory && s.afterInventory != nil {
		after := s.afterInventory
		s.afterInventory = nil
		after()
	}
	return result, err
}

func TestCurrentSessionIdentityOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	// Use one canonical parent so tmux's socket_path and the ticket-33
	// fixture's resolved socket agree on macOS, where /tmp is a symlink.
	parent, err := os.MkdirTemp("/tmp", "sesh-current-")
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
		"rename race", "literal ID name", "unmarked bridge-like name", "marked current",
		"disappeared after query", "disappeared after ownership",
		"restart after inventory", "restart after query", "restart after ownership",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture, native := newBridgePrivateFixture(t, binary)
			require.NoError(t, fixture.startAnchor())
			native.sessionID = "$1"
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
			run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/anchor")
			anchorBefore := state("$0")
			pathA, pathB := filepath.Join(fixture.dir, "A"), filepath.Join(fixture.dir, "B")
			for _, path := range []string{pathA, pathB} {
				require.NoError(t, os.Mkdir(path, 0700))
			}
			name := "a"
			if mode == "literal ID name" {
				name = "$0"
			} else if mode == "unmarked bridge-like name" {
				name = "sesh-ssh-user"
			}
			require.Equal(t, "$1", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", name, "-c", pathA, "--", "exec sleep 120"))
			require.Equal(t, "$2", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "b", "-c", pathB, "--", "exec sleep 120"))
			run("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, "")
			if mode == "marked current" {
				run("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, "ssh/fixture/work")
			} else if mode == "unmarked bridge-like name" {
				// Absence in the owning session must not inherit a global marker.
				run("set-environment", "-u", "-t", "$1", model.SSHBridgeIdentityEnv)
				run("set-environment", "-g", model.SSHBridgeIdentityEnv, "ssh/global/ignored")
			}
			sh := &currentSessionPrivateShell{panePrivateShell: native}
			if mode == "rename race" {
				sh.afterInventory = func() {
					run("rename-session", "-t", "$2:", "renamed-away")
					run("rename-session", "-t", "$1:", "b")
				}
			}
			var replacementBefore []string
			change := func() {
				if strings.HasPrefix(mode, "restart") {
					require.NoError(t, fixture.stop())
					require.NoError(t, fixture.startAnchor())
					require.NotEqual(t, fixture.starts[0].Generation, fixture.starts[len(fixture.starts)-1].Generation)
					require.Equal(t, "$1", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "replacement", "-c", pathB, "--", "exec sleep 120"))
					if mode == "restart after inventory" {
						// Keep every old inventory ID readable so the generation
						// comparison, rather than a missing marker lookup, rejects it.
						require.Equal(t, "$2", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "b", "-c", pathB, "--", "exec sleep 120"))
					}
					anchorBefore = state("$0")
					replacementBefore = state("$1")
				} else {
					run("kill-session", "-t", "$1:")
					// Even an ID-looking namesake cannot replace the vanished ID.
					run("rename-session", "-t", "$2:", "$1")
					replacementBefore = state("$2")
				}
			}
			if strings.HasSuffix(mode, "after inventory") {
				sh.afterInventory = change
			} else if strings.HasSuffix(mode, "after query") {
				sh.afterCurrent = change
			} else if strings.HasSuffix(mode, "after ownership") {
				sh.afterOwnership = change
			}
			mOs := oswrap.NewMockOs(t)
			mOs.EXPECT().Getenv("TMUX").Return(native.socket + ",1,1").Once()
			tm := tmux.NewTmux(mOs, sh, "private-tmux")
			li := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)

			session, found := li.GetAttachedTmuxSession()

			if mode == "marked current" || strings.HasPrefix(mode, "disappeared") || strings.HasPrefix(mode, "restart") {
				require.False(t, found)
				require.Equal(t, model.SeshSession{}, session)
			} else {
				require.True(t, found)
				require.Equal(t, "$1", session.TmuxSessionID)
				require.Equal(t, fixture.starts[0].Generation, session.TmuxServerGeneration)
				resolvedA, err := filepath.EvalSymlinks(pathA)
				require.NoError(t, err)
				require.Equal(t, resolvedA, session.Path)
				require.NotEqual(t, pathB, session.Path)
				// Exercise the same path consumer used by sesh root.
				g := git.NewMockGit(t)
				g.EXPECT().WorktreeList(resolvedA).Return(false, "", nil).Once()
				root, err := namer.NewNamer(pathwrap.NewPath(), g, nil, model.Config{}).RootName(session.Path)
				require.NoError(t, err)
				require.Equal(t, "A", root)
				if mode == "rename race" {
					require.Equal(t, "b", run("display-message", "-p", "-t", "$1:", "#{session_name}"))
					require.Equal(t, "renamed-away", run("display-message", "-p", "-t", "$2:", "#{session_name}"))
				}
			}
			require.Equal(t, anchorBefore, state("$0"), "the marked bridge remains untouched")
			if replacementBefore != nil {
				id := "$2"
				if strings.HasPrefix(mode, "restart") {
					id = "$1"
				}
				require.Equal(t, replacementBefore, state(id), "no substitute session is touched")
			}
		})
	}
}
