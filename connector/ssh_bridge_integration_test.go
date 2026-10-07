//go:build unix

package connector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

// Only restart timing and the SSH transport are injected. Verification,
// generation checks, destruction, and replacement creation use real tmux.
type bridgeCleanupTmux struct {
	tmux.Tmux
	beforeCleanup func()
	created       int
}

func (b *bridgeCleanupTmux) cleanupBoundary() {
	if b.beforeCleanup != nil {
		before := b.beforeCleanup
		b.beforeCleanup = nil
		before()
	}
}

func (b *bridgeCleanupTmux) KillSession(id string) (string, error) {
	// Keep the injected restart at the same boundary if production regresses
	// to the unguarded API, so the regression observes actual ID reuse.
	b.cleanupBoundary()
	return b.Tmux.KillSession(id)
}

func (b *bridgeCleanupTmux) KillSessionInGeneration(id, generation string) (string, error) {
	b.cleanupBoundary()
	return b.Tmux.KillSessionInGeneration(id, generation)
}

func (b *bridgeCleanupTmux) NewSessionWithCommand(opts model.TmuxSessionOpts) (string, error) {
	b.created++
	opts.Command = "printf 'STUB_SSH_OUTPUT\\n'; exec sleep 120"
	return b.Tmux.NewSessionWithCommand(opts)
}

func TestSSHDeadBridgeCleanupOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, mode := range []string{"same generation", "server restarted", "missing generation", "invalid generation", "bridge disappeared"} {
		t.Run(mode, func(t *testing.T) {
			fixture, sh := newBridgePrivateFixture(t, binary)
			dir := fixture.dir
			run := func(args ...string) string {
				t.Helper()
				out, err := sh.run(args...)
				require.NoError(t, err)
				return out
			}
			require.NoError(t, fixture.startAnchor())
			wrapper := filepath.Join(dir, "tmux")
			require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+shell.Quote(binary)+
				" -S "+shell.Quote(sh.socket)+" -f /dev/null \"$@\"\n"), 0700))
			realTmux := tmux.NewTmux(nil, sh, wrapper)
			const identity = "ssh/fixture/work"
			created, err := realTmux.NewSessionWithCommand(model.TmuxSessionOpts{
				Name: "renamed-dead-bridge", Command: "printf 'DEAD_BRIDGE\\n'; exit 23",
				Environment: []string{model.SSHBridgeIdentityEnv + "=" + identity},
				PaneOptions: []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}},
			})
			require.NoError(t, err)
			require.Equal(t, "$1", strings.Fields(created)[0])
			require.Eventually(t, func() bool {
				dead, err := realTmux.IsPaneDead("$1")
				return err == nil && dead
			}, 3*time.Second, 10*time.Millisecond)
			sessions, err := realTmux.ListSessions()
			require.NoError(t, err)
			var bridge *model.TmuxSession
			for _, session := range sessions {
				if session.ID == "$1" {
					bridge = session
				}
			}
			require.NotNil(t, bridge)
			require.NotEmpty(t, bridge.ServerGeneration)
			require.Equal(t, identity, bridge.SSHBridgeIdentity)
			originalGeneration := bridge.ServerGeneration
			state := func(id string) []string {
				t.Helper()
				return []string{
					run("display-message", "-p", "-t", id+":", "#{session_id} #{session_name} #{window_id} #{pane_id} #{pane_pid}"),
					run("list-windows", "-t", id+":", "-F", "#{window_id} #{window_active} #{pane_id}"),
					run("show-environment", "-t", id),
					run("capture-pane", "-p", "-t", id+":"),
				}
			}
			var unrelatedBefore []string
			guarded := &bridgeCleanupTmux{Tmux: realTmux}
			guarded.beforeCleanup = func() {
				if mode == "server restarted" {
					require.NoError(t, fixture.stop(), "generation A must be proven stopped before restarting")
					require.NoError(t, fixture.startAnchor())
					require.Equal(t, "$1", run("new-session", "-d", "-P", "-F", "#{session_id}",
						"-s", "unrelated-user", "--", "printf 'UNRELATED_OUTPUT\\n'; exec sleep 120"))
					run("new-window", "-d", "-t", "$1:", "-n", "inactive", "--", "exec sleep 120")
					require.Eventually(t, func() bool {
						return strings.Contains(run("capture-pane", "-p", "-t", "$1:"), "UNRELATED_OUTPUT")
					}, 3*time.Second, 10*time.Millisecond)
					require.NotEqual(t, originalGeneration,
						run("display-message", "-p", "-t", "$1:", "#{pid}:#{start_time}:#{socket_path}"))
					marker, err := realTmux.GetSessionEnvironment("$1", model.SSHBridgeIdentityEnv)
					require.NoError(t, err)
					require.Empty(t, marker, "generation B's session is an unmarked user session")
					unrelatedBefore = state("$1")
				} else if mode == "bridge disappeared" {
					_, err := realTmux.KillSessionInGeneration("$1", originalGeneration)
					require.NoError(t, err)
				}
			}
			// Alter only inventory metadata for unknown-generation cases; the
			// real guarded API must reject without sending any destructive command.
			if mode == "missing generation" || mode == "invalid generation" {
				if mode == "missing generation" {
					bridge.ServerGeneration = ""
				} else {
					bridge.ServerGeneration = "invalid"
				}
				inventory := tmux.NewMockTmux(t)
				inventory.EXPECT().ListSessions().Return(sessions, nil).Once()
				// The inventory seam leaves marker, dead-pane inspection, and kill
				// on real tmux, preserving the actual connector cleanup path.
				guarded.Tmux = &bridgeInventoryTmux{Tmux: realTmux, inventory: inventory}
			}
			anchorBefore := state("$0")
			c := &RealConnector{tmux: guarded}

			id, err := c.ensureSSHBridge(identity)

			if mode != "same generation" {
				require.ErrorContains(t, err, "failed to remove stale SSH bridge")
				require.Empty(t, id)
				require.Zero(t, guarded.created, "rejected cleanup must not create another bridge")
				if mode == "server restarted" {
					require.ErrorContains(t, err, "tmux server generation changed")
					require.Equal(t, unrelatedBefore, state("$1"), "unrelated ID reuse must remain untouched")
				} else {
					require.Equal(t, anchorBefore, state("$0"))
					if mode != "bridge disappeared" {
						require.Equal(t, "renamed-dead-bridge", run("display-message", "-p", "-t", "$1:", "#{session_name}"))
					}
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, "$2", id)
			require.Equal(t, 1, guarded.created)
			require.Equal(t, []string{"$0", "$2"}, strings.Fields(run("list-sessions", "-F", "#{session_id}")))
			marker, err := realTmux.GetSessionEnvironment(id, model.SSHBridgeIdentityEnv)
			require.NoError(t, err)
			require.Equal(t, identity, marker)
			for _, option := range []string{"status", "detach-on-destroy"} {
				require.Equal(t, "off", run("show-options", "-v", "-t", id, option))
			}
			require.Equal(t, "failed", run("show-options", "-p", "-v", "-t", id+":", "remain-on-exit"))
			reused, err := c.ensureSSHBridge(identity)
			require.NoError(t, err)
			require.Equal(t, id, reused)
			require.Equal(t, 1, guarded.created, "a live replacement is reused")
			require.Equal(t, anchorBefore, state("$0"), "unrelated anchor remains untouched")
		})
	}
}

type bridgeInventoryTmux struct {
	tmux.Tmux
	inventory tmux.Tmux
}

func (b *bridgeInventoryTmux) ListSessions() ([]*model.TmuxSession, error) {
	return b.inventory.ListSessions()
}
