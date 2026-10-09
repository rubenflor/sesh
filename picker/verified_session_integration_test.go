//go:build unix

package picker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/joshmedeski/sesh/v2/cache"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

// Every command reaches a real server on this fixture's private socket. The
// shell dependency only supplies isolation and deadlines, not tmux responses.
type deletionPrivateShell struct {
	shell.Shell
	binary string
	socket string
	dir    string
	pids   []int
	kills  []string
}

func newDeletionPrivateShell(t *testing.T, binary string) *deletionPrivateShell {
	t.Helper()
	// Keep fixture state beneath a caller's isolated TMPDIR, and keep the
	// socket path short enough for Unix-domain sockets on macOS.
	dir, err := os.MkdirTemp(os.TempDir(), "sesh-kill-")
	require.NoError(t, err)
	sh := &deletionPrivateShell{binary: binary, socket: filepath.Join(dir, "socket"), dir: dir}
	t.Cleanup(func() {
		_, killErr := sh.run("kill-server")
		if killErr != nil && len(sh.pids) != 0 && syscall.Kill(sh.pids[len(sh.pids)-1], 0) == nil {
			t.Errorf("private tmux cleanup: %v", killErr)
		}
		deadline := time.Now().Add(3 * time.Second)
		allGone := true
		for _, pid := range sh.pids {
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				t.Errorf("private tmux server %d remains after cleanup: %v", pid, err)
				allGone = false
			}
		}
		if !allGone {
			return // Retain the known socket if any server did not exit.
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove private tmux fixture: %v", err)
		}
	})
	return sh
}

func (s *deletionPrivateShell) recordPID(t *testing.T, rawPID string) int {
	t.Helper()
	pid, err := strconv.Atoi(rawPID)
	require.NoError(t, err)
	s.pids = append(s.pids, pid)
	var recorded strings.Builder
	for _, pid := range s.pids {
		fmt.Fprintf(&recorded, "%d\n", pid)
	}
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "server.pid"), []byte(recorded.String()), 0600))
	return pid
}

func (s *deletionPrivateShell) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.binary, append([]string{"-S", s.socket, "-f", "/dev/null"}, args...)...)
	cmd.WaitDelay = 250 * time.Millisecond
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if name != "TMUX" && name != "TMUX_PANE" && name != "SESH_CLIENT" {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := strings.TrimSuffix(stdout.String(), "\n")
	if err != nil {
		return out, fmt.Errorf("private tmux %v: %w: %s", args, err, stderr.String())
	}
	return out, nil
}

func (s *deletionPrivateShell) recordKill(args []string) {
	if len(args) == 3 && args[0] == "kill-session" && args[1] == "-t" {
		s.kills = append(s.kills, args[2])
	} else if len(args) >= 4 && args[0] == "if-shell" && args[1] == "-F" && args[2] == "-t" {
		s.kills = append(s.kills, args[3])
	}
}

func (s *deletionPrivateShell) Cmd(_ string, args ...string) (string, error) {
	s.recordKill(args)
	return s.run(args...)
}

func (s *deletionPrivateShell) CmdCapture(_ string, args ...string) (string, error) {
	s.recordKill(args)
	return s.run(args...)
}

func (s *deletionPrivateShell) CmdCaptureRaw(ctx context.Context, _ string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args = append([]string{"-u", "TMUX", "-u", "TMUX_PANE", "-u", "SESH_CLIENT", s.binary, "-S", s.socket, "-f", "/dev/null"}, args...)
	return shell.NewShell(execwrap.NewExec(), nil).CmdCaptureRaw(ctx, "/usr/bin/env", stdoutLimit, stderrLimit, args...)
}

func (s *deletionPrivateShell) ListCmd(_ string, args ...string) ([]string, error) {
	out, err := s.run(args...)
	return strings.Split(out, "\n"), err
}

func TestPickerDeletionUsesVerifiedIDOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, fixture := range []struct {
		name   string
		rename bool
	}{{name: "$0"}, {name: "$1"}, {name: "ordinary"}, {name: "sesh-ssh-user"}, {name: "$0", rename: true}} {
		testName := fixture.name
		if fixture.rename {
			testName += "-renamed-after-confirm"
		}
		t.Run(testName, func(t *testing.T) {
			name := fixture.name
			sh := newDeletionPrivateShell(t, binary)
			if name == "ordinary" {
				// Generation metadata and native format comparison must preserve
				// socket names containing format delimiters and escaped literals.
				sh.socket = filepath.Join(sh.dir, "socket,#[x]#{x}#}::")
			}
			run := func(args ...string) string {
				t.Helper()
				out, err := sh.run(args...)
				require.NoError(t, err)
				return out
			}
			created := strings.Fields(run("new-session", "-d", "-P", "-F", "#{pid} #{session_id}", "-s", "bridge", "-n", "bridge", "--", "/bin/sh", "-c", "printf 'BRIDGE_OUTPUT\\n'; exec sleep 60"))
			require.Len(t, created, 2)
			sh.recordPID(t, created[0])
			require.Equal(t, "$0", created[1])
			run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/host/work")
			require.Equal(t, "$1", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", name, "--", "/bin/sh", "-c", "printf 'USER_OUTPUT\\n'; exec sleep 60"))
			require.Eventually(t, func() bool {
				return strings.Contains(run("capture-pane", "-p", "-t", "$0"), "BRIDGE_OUTPUT") &&
					strings.Contains(run("capture-pane", "-p", "-t", "$1"), "USER_OUTPUT")
			}, 2*time.Second, 10*time.Millisecond)
			bridgeState := run("display-message", "-p", "-t", "$0", "#{session_id} #{session_name} #{pane_id} #{pane_pid} #{window_id} #{window_active} #{pane_active}")
			bridgeMarker := run("show-environment", "-t", "$0", model.SSHBridgeIdentityEnv)
			bridgeOutput := run("capture-pane", "-p", "-t", "$0")

			tm := tmux.NewTmux(nil, sh, binary)
			live := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			sessions, err := live.List(lister.ListOptions{Tmux: true})
			require.NoError(t, err)
			require.Len(t, sessions.OrderedIndex, 1, "marked bridges must remain hidden")
			selected := sessions.Directory[sessions.OrderedIndex[0]]
			require.Equal(t, name, selected.Name)
			require.Equal(t, "$1", selected.TmuxSessionID)
			require.NotEmpty(t, selected.TmuxServerGeneration)
			fileCache := cache.NewFileCacheWithPath(filepath.Join(sh.dir, "sessions.gob"))
			require.NoError(t, fileCache.Write(sessions))
			cached, err := fileCache.Read()
			require.NoError(t, err)
			require.Equal(t, selected, cached.Sessions.Directory[sessions.OrderedIndex[0]], "gob must retain the selected ID and generation")
			sessions = cached.Sessions

			refreshes := 0
			p := &RealPicker{tmux: tm, refreshCache: func() { refreshes++ }}
			opts := testOptionsWith(func(o *Options) { o.Kill = p.killSession })
			loaded, _ := New(testFetchFunc(sessions), opts).Update(sessionsLoadedMsg{sessions: sessions})
			m := cursorOnSrc(t, loaded.(Model), tmuxSrc)
			m = press(m, ctrlX)
			require.NotNil(t, m.confirm)
			require.Equal(t, name, m.confirm.name, "confirmation must keep the public session name")
			var replacementState, replacementOutput string
			if fixture.rename {
				// Identity is captured from the visible row, before confirmation.
				// Another session reusing that row's name cannot replace its target.
				run("rename-session", "-t", "$1", "renamed-local")
				renamed, err := live.List(lister.ListOptions{Tmux: true})
				require.NoError(t, err)
				require.Len(t, renamed.OrderedIndex, 1)
				require.Equal(t, selected.TmuxServerGeneration, renamed.Directory[renamed.OrderedIndex[0]].TmuxServerGeneration, "same-server rename must retain generation")
				require.Equal(t, "$2", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", name, "--", "/bin/sh", "-c", "printf 'REPLACEMENT_OUTPUT\\n'; exec sleep 60"))
				require.Eventually(t, func() bool {
					return strings.Contains(run("capture-pane", "-p", "-t", "$2"), "REPLACEMENT_OUTPUT")
				}, 2*time.Second, 10*time.Millisecond)
				replacementState = run("display-message", "-p", "-t", "$2", "#{session_id} #{session_name} #{pane_id} #{pane_pid}")
				replacementOutput = run("capture-pane", "-p", "-t", "$2")
			}
			confirmed, cmd := m.Update(tea.KeyPressMsg{Code: 'y'})
			require.NotNil(t, cmd)
			msg := cmd().(entryRemovedMsg)
			require.NoError(t, msg.err)
			m = press(confirmed.(Model), msg)
			require.Empty(t, m.allItems)
			require.Empty(t, m.filtered)
			require.Equal(t, 1, refreshes)
			require.Equal(t, []string{"$1:"}, sh.kills, "deletion must target the selected row's server ID")

			// has-session -t '$0' could fall back to a surviving literal name.
			// Inventory proves exactly which server-assigned session ID remains.
			ids := strings.Split(run("list-sessions", "-F", "#{session_id}"), "\n")
			if fixture.rename {
				require.ElementsMatch(t, []string{"$0", "$2"}, ids)
				require.Equal(t, replacementState, run("display-message", "-p", "-t", "$2", "#{session_id} #{session_name} #{pane_id} #{pane_pid}"))
				require.Equal(t, replacementOutput, run("capture-pane", "-p", "-t", "$2"))
			} else {
				require.Equal(t, []string{"$0"}, ids)
			}
			require.Equal(t, bridgeState, run("display-message", "-p", "-t", "$0", "#{session_id} #{session_name} #{pane_id} #{pane_pid} #{window_id} #{window_active} #{pane_active}"))
			require.Equal(t, bridgeMarker, run("show-environment", "-t", "$0", model.SSHBridgeIdentityEnv))
			require.Equal(t, bridgeOutput, run("capture-pane", "-p", "-t", "$0"))
			remaining, err := live.List(lister.ListOptions{Tmux: true})
			require.NoError(t, err)
			if fixture.rename {
				require.Len(t, remaining.OrderedIndex, 1)
				require.Equal(t, "$2", remaining.Directory[remaining.OrderedIndex[0]].TmuxSessionID)
			} else {
				require.Empty(t, remaining.OrderedIndex, "a surviving marked bridge must stay hidden")
			}
		})
	}
}

// Return the inventory from the old server, then restart before the picker
// invokes the native kill. The tmux generation guard must cover this gap too.
type deletionRestartTmux struct {
	tmux.Tmux
	afterList func()
}

func (tm *deletionRestartTmux) ListSessions() ([]*model.TmuxSession, error) {
	sessions, err := tm.Tmux.ListSessions()
	if err == nil && tm.afterList != nil {
		restart := tm.afterList
		tm.afterList = nil
		restart()
	}
	return sessions, err
}

func TestPickerDeletionRejectsReusedIDAfterPrivateTmuxRestart(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, afterValidation := range []bool{false, true} {
		name := "before-live-validation"
		if afterValidation {
			name = "after-live-validation"
		}
		t.Run(name, func(t *testing.T) {
			sh := newDeletionPrivateShell(t, binary)
			run := func(args ...string) string {
				t.Helper()
				out, err := sh.run(args...)
				require.NoError(t, err)
				return out
			}
			created := strings.Fields(run("new-session", "-d", "-P", "-F", "#{pid} #{session_id}", "-s", "old-bridge", "--", "/bin/sh", "-c", "exec sleep 60"))
			require.Len(t, created, 2)
			oldPID := sh.recordPID(t, created[0])
			require.Equal(t, "$0", created[1])
			run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/host/old")
			require.Equal(t, "$1", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "old-work", "--", "/bin/sh", "-c", "exec sleep 60"))

			tm := tmux.NewTmux(nil, sh, binary)
			live := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			sessions, err := live.List(lister.ListOptions{Tmux: true})
			require.NoError(t, err)
			require.Len(t, sessions.OrderedIndex, 1)
			selected := sessions.Directory[sessions.OrderedIndex[0]]
			require.Equal(t, "old-work", selected.Name)
			require.Equal(t, "$1", selected.TmuxSessionID)
			require.NotEmpty(t, selected.TmuxServerGeneration)
			cachePath := filepath.Join(sh.dir, "sessions.gob")
			fileCache := cache.NewFileCacheWithPath(cachePath)
			require.NoError(t, fileCache.Write(sessions))
			cached, err := fileCache.Read()
			require.NoError(t, err)
			require.Equal(t, selected, cached.Sessions.Directory[sessions.OrderedIndex[0]], "cached selection must retain the old server generation")
			cacheBefore, err := os.ReadFile(cachePath)
			require.NoError(t, err)

			const stateFormat = "#{session_id} #{session_name} #{pane_id} #{pane_pid} #{window_id} #{window_active} #{pane_active}"
			var inventory, bridgeState, bridgeMarker, bridgeOutput, userState, userOutput, newGeneration string
			restarts := 0
			restart := func() {
				restarts++
				run("kill-server")
				require.Eventually(t, func() bool { return syscall.Kill(oldPID, 0) == syscall.ESRCH }, 3*time.Second, 10*time.Millisecond, "old server must exit before the socket is reused")
				created := strings.Fields(run("new-session", "-d", "-P", "-F", "#{pid} #{session_id}", "-s", "bridge", "-n", "bridge", "--", "/bin/sh", "-c", "printf 'BRIDGE_OUTPUT\\n'; exec sleep 60"))
				require.Len(t, created, 2)
				sh.recordPID(t, created[0])
				require.Equal(t, "$0", created[1])
				run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/host/new")
				require.Equal(t, "$1", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "unrelated-work", "--", "/bin/sh", "-c", "printf 'USER_OUTPUT\\n'; exec sleep 60"))
				require.Eventually(t, func() bool {
					return strings.Contains(run("capture-pane", "-p", "-t", "$0"), "BRIDGE_OUTPUT") &&
						strings.Contains(run("capture-pane", "-p", "-t", "$1"), "USER_OUTPUT")
				}, 2*time.Second, 10*time.Millisecond)
				inventory = run("list-sessions", "-F", "#{session_id} #{session_name}")
				require.ElementsMatch(t, []string{"$0 bridge", "$1 unrelated-work"}, strings.Split(inventory, "\n"))
				bridgeState = run("display-message", "-p", "-t", "$0", stateFormat)
				bridgeMarker = run("show-environment", "-t", "$0", model.SSHBridgeIdentityEnv)
				bridgeOutput = run("capture-pane", "-p", "-t", "$0")
				userState = run("display-message", "-p", "-t", "$1", stateFormat)
				userOutput = run("capture-pane", "-p", "-t", "$1")
				marker, err := tm.GetSessionEnvironment("$1", model.SSHBridgeIdentityEnv)
				require.NoError(t, err)
				require.Empty(t, marker)
				current, err := live.List(lister.ListOptions{Tmux: true})
				require.NoError(t, err)
				require.Len(t, current.OrderedIndex, 1, "restarted bridge must remain hidden")
				newGeneration = current.Directory[current.OrderedIndex[0]].TmuxServerGeneration
				require.NotEmpty(t, newGeneration)
				require.NotEqual(t, selected.TmuxServerGeneration, newGeneration, "reused session ID belongs to another server generation")
			}

			var killTmux tmux.Tmux = tm
			if afterValidation {
				killTmux = &deletionRestartTmux{Tmux: tm, afterList: restart}
			}
			refreshes := 0
			p := &RealPicker{tmux: killTmux, refreshCache: func() {
				refreshes++
				current, err := live.List(lister.ListOptions{Tmux: true})
				require.NoError(t, err)
				require.NoError(t, fileCache.Write(current))
			}}
			opts := testOptionsWith(func(o *Options) { o.Kill = p.killSession })
			loaded, _ := New(testFetchFunc(cached.Sessions), opts).Update(sessionsLoadedMsg{sessions: cached.Sessions})
			m := press(cursorOnSrc(t, loaded.(Model), tmuxSrc), ctrlX)
			require.NotNil(t, m.confirm)
			require.Equal(t, "old-work", m.confirm.name)
			if !afterValidation {
				restart()
			}
			confirmed, cmd := m.Update(tea.KeyPressMsg{Code: 'y'})
			require.NotNil(t, cmd)
			msg := cmd().(entryRemovedMsg)
			require.Error(t, msg.err, "cached $1 must never delete another server's $1")
			m = press(confirmed.(Model), msg)
			require.Len(t, m.allItems, 1)
			require.Len(t, m.filtered, 1)
			require.Equal(t, "old-work", m.allItems[0].name)
			require.Equal(t, 1, restarts)
			require.Zero(t, refreshes, "failed deletion must not refresh the cache")
			if afterValidation {
				require.Equal(t, []string{"$1:"}, sh.kills, "native generation guard must reject the server restart after validation")
			} else {
				require.Empty(t, sh.kills, "live generation mismatch must fail before a kill command")
			}

			require.Equal(t, inventory, run("list-sessions", "-F", "#{session_id} #{session_name}"))
			require.Equal(t, bridgeState, run("display-message", "-p", "-t", "$0", stateFormat))
			require.Equal(t, bridgeMarker, run("show-environment", "-t", "$0", model.SSHBridgeIdentityEnv))
			require.Equal(t, bridgeOutput, run("capture-pane", "-p", "-t", "$0"))
			require.Equal(t, userState, run("display-message", "-p", "-t", "$1", stateFormat))
			require.Equal(t, userOutput, run("capture-pane", "-p", "-t", "$1"))
			marker, err := tm.GetSessionEnvironment("$1", model.SSHBridgeIdentityEnv)
			require.NoError(t, err)
			require.Empty(t, marker)
			remaining, err := live.List(lister.ListOptions{Tmux: true})
			require.NoError(t, err)
			require.Len(t, remaining.OrderedIndex, 1)
			unrelated := remaining.Directory[remaining.OrderedIndex[0]]
			require.Equal(t, "unrelated-work", unrelated.Name)
			require.Equal(t, "$1", unrelated.TmuxSessionID)
			require.Equal(t, newGeneration, unrelated.TmuxServerGeneration)
			cacheAfter, err := os.ReadFile(cachePath)
			require.NoError(t, err)
			require.Equal(t, cacheBefore, cacheAfter, "failed deletion must preserve the cached old row")
		})
	}
}
