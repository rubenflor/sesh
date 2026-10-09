package connector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/stretchr/testify/require"
)

// Reuse the bounded private-server runner. Only terminal/client handoff is
// injected; each handed-off target is resolved by that same real tmux server.
type tmuxinatorPrivateShell struct {
	*panePrivateShell
	handoffs [][]string
	resolved string
	lists    int
}

func (s *tmuxinatorPrivateShell) Cmd(cmd string, args ...string) (string, error) {
	if args[0] == "attach-session" || args[0] == "switch-client" {
		s.handoffs = append(s.handoffs, append([]string(nil), args...))
		out, err := s.run("display-message", "-p", "-t", args[len(args)-1], "#{session_id}")
		s.resolved = out
		return out, err
	}
	return s.panePrivateShell.Cmd(cmd, args...)
}

func (s *tmuxinatorPrivateShell) CmdWithOutput(cmd string, args ...string) (string, error) {
	return s.Cmd(cmd, args...)
}

func (s *tmuxinatorPrivateShell) CmdCaptureRaw(ctx context.Context, cmd string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
	if args[0] == "-u" && args[1] == "list-sessions" {
		s.lists++
	}
	return s.panePrivateShell.CmdCaptureRaw(ctx, cmd, stdoutLimit, stderrLimit, args...)
}

func TestTmuxinatorConnectUsesVerifiedIDOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, name := range []string{"$0", "$1", "$2", "ordinary", "sesh-ssh-user"} {
		for _, mode := range []struct {
			name     string
			attached bool
			switchTo bool
		}{
			{"attach", false, false},
			{"attached switch", true, false},
			{"external switch", false, true},
		} {
			t.Run(name+"/"+mode.name, func(t *testing.T) {
				// Keep the private socket path short enough for macOS.
				dir, err := os.MkdirTemp("/tmp", "sesh-tmuxinator-")
				require.NoError(t, err)
				sh := &tmuxinatorPrivateShell{panePrivateShell: &panePrivateShell{binary: binary, socket: filepath.Join(dir, "socket")}}
				t.Cleanup(func() {
					if _, err := sh.run("kill-server"); err != nil {
						t.Errorf("private tmux cleanup: %v", err)
					}
					if err := os.RemoveAll(dir); err != nil {
						t.Errorf("remove private tmux fixture: %v", err)
					}
				})
				run := func(args ...string) string {
					t.Helper()
					out, err := sh.run(args...)
					require.NoError(t, err)
					return out
				}
				require.Equal(t, "$0", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "bridge", "-n", "active-bridge", "-c", dir, "--", "/bin/sh", "-c", "printf 'BRIDGE_OUTPUT\\n'; exec sleep 120"))
				run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/host/work")
				run("new-window", "-d", "-t", "$0:", "-n", "inactive", "--", "/bin/sh", "-c", "exec sleep 120")
				require.Eventually(t, func() bool {
					return strings.Contains(run("capture-pane", "-p", "-t", "$0"), "BRIDGE_OUTPUT")
				}, 2*time.Second, 10*time.Millisecond)
				bridgeState := func() []string {
					return []string{
						run("display-message", "-p", "-t", "$0", "#{session_name} #{session_windows} #{window_id} #{window_name} #{pane_id} #{pane_pid}"),
						run("list-windows", "-t", "$0", "-F", "#{window_id} #{window_active} #{pane_id}"),
						run("show-environment", "-t", "$0", model.SSHBridgeIdentityEnv),
						run("capture-pane", "-p", "-t", "$0"),
					}
				}
				before := bridgeState()
				mOs := oswrap.NewMockOs(t)
				attached := ""
				if mode.attached {
					attached = sh.socket + ",1,0"
					mOs.EXPECT().Getenv("SESH_CLIENT").Return("")
					mOs.EXPECT().Getenv("TMUX_PANE").Return("%0")
				} else if mode.switchTo {
					mOs.EXPECT().Getenv("SESH_CLIENT").Return("client")
				}
				mOs.EXPECT().Getenv("TMUX").Return(attached)
				tm := tmux.NewTmux(mOs, sh, "private-tmux")
				project := tmuxinator.NewMockTmuxinator(t)
				project.EXPECT().List().Return([]*model.TmuxinatorConfig{{Name: name}}, nil).Once()
				project.EXPECT().Start(name).RunAndReturn(func(startName string) (string, error) {
					id, err := sh.run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", startName, "-c", dir, "--", "/bin/sh", "-c", "exec sleep 120")
					require.NoError(t, err)
					require.Equal(t, "$1", id)
					sh.lists = 0
					return "", nil
				}).Once()
				li := lister.NewLister(model.Config{}, nil, tm, nil, project, nil, nil, nil)
				c := NewConnector(model.Config{}, nil, nil, li, nil, nil, tm, nil, project, nil)

				message, err := c.Connect(name, model.ConnectOpts{Switch: mode.switchTo})

				require.NoError(t, err)
				require.Equal(t, "$1", sh.resolved, "real tmux resolves the operational target to the user session")
				require.Equal(t, 1, sh.lists, "resolve the created session once after startup")
				verb, handoff := "attaching", []string{"attach-session", "-t", "$1"}
				if mode.attached || mode.switchTo {
					verb, handoff = "switching", []string{"switch-client", "-t", "$1"}
					if mode.switchTo {
						handoff = []string{"switch-client", "-c", "client", "-t", "$1"}
					}
				}
				require.Equal(t, verb+" to tmux session: "+name, message)
				require.Equal(t, [][]string{handoff}, sh.handoffs)
				require.Equal(t, name, run("display-message", "-p", "-t", "$1", "#{session_name}"))
				require.Equal(t, before, bridgeState(), "bridge name, marker, active windows/panes and output stay unchanged")
				visible, found := li.FindTmuxSession(name)
				require.True(t, found, "unmarked user names remain visible")
				require.Equal(t, "$1", visible.TmuxSessionID)
				_, found = li.FindTmuxSession("bridge")
				require.False(t, found, "marked bridge stays hidden")
			})
		}
	}
}
