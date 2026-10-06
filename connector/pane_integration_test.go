package connector

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only the calling session is injected; inventory, ownership and selection use
// real tmux target resolution on the private server.
type panePrivateShell struct {
	shell.Shell
	binary, socket, sessionID string
}

func (s *panePrivateShell) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.binary, append([]string{"-S", s.socket, "-f", "/dev/null"}, args...)...)
	cmd.WaitDelay = 250 * time.Millisecond
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		switch name {
		case "TMUX", "TMUX_PANE", "SESH_CLIENT", "SHELL", model.SSHBridgeIdentityEnv:
		default:
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "SHELL=/bin/sh")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := strings.TrimSuffix(stdout.String(), "\n")
	if err != nil {
		return out, fmt.Errorf("private tmux %v: %w: %s", args, err, stderr.String())
	}
	return out, nil
}

func (s *panePrivateShell) Cmd(_ string, args ...string) (string, error) {
	if args[0] == "display-message" && len(args) == 3 {
		args = append([]string{"display-message", "-t", s.sessionID}, args[1:]...)
	}
	return s.run(args...)
}

func (s *panePrivateShell) CmdCapture(_ string, args ...string) (string, error) {
	return s.run(args...)
}

func (s *panePrivateShell) ListCmd(_ string, args ...string) ([]string, error) {
	if args[0] == "list-panes" {
		args = append(args, "-t", s.sessionID)
	}
	out, err := s.run(args...)
	return strings.Split(out, "\n"), err
}

func TestPaneConnectUsesVerifiedIDsOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, name := range []string{"$0", "$1", "$2", "ordinary", "sesh-ssh-user"} {
		t.Run(name, func(t *testing.T) {
			// Keep the private socket path short enough for macOS.
			dir, err := os.MkdirTemp("/tmp", "sesh-pane-")
			require.NoError(t, err)
			sh := &panePrivateShell{binary: binary, socket: filepath.Join(dir, "socket"), sessionID: "$1"}
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
			for i, sessionName := range []string{"bridge", name} {
				id := fmt.Sprintf("$%d", i)
				require.Equal(t, id, run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", sessionName, "-n", "initial", "-c", dir, "--", "/bin/sh", "-c", "exec sleep 120"))
				if i == 0 {
					run("set-environment", "-t", id, model.SSHBridgeIdentityEnv, "ssh/host/work")
				}
				// Both sessions have the same window/pane indexes, but distinct IDs.
				run("new-window", "-d", "-t", id+":", "-n", "code", "--", "/bin/sh", "-c", "exec sleep 120")
				run("split-window", "-d", "-t", id+":1", "--", "/bin/sh", "-c", "exec sleep 120")
				for pane := 0; pane < 2; pane++ {
					run("select-pane", "-t", fmt.Sprintf("%s:1.%d", id, pane), "-T", "editor")
				}
			}
			active := func(id string) string {
				return run("display-message", "-p", "-t", id, "#{session_id} #{window_id} #{pane_id}")
			}
			bridgeBefore := active("$0")
			bridgePanesBefore := run("list-windows", "-t", "$0", "-F", "#{window_id} #{window_active} #{pane_id}")
			intended := active("$1:1.1")
			require.NotEqual(t, intended, active("$1"), "user starts in another window")
			require.NotEqual(t, intended, active("$1:1"), "user starts with another active pane")
			require.Equal(t, name, run("display-message", "-p", "-t", "$1", "#{session_name}"))

			mOs := oswrap.NewMockOs(t)
			mOs.EXPECT().Getenv("TMUX").Return(sh.socket + ",1,1")
			tm := tmux.NewTmux(mOs, sh, "private-tmux")
			li := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			listed, err := li.ListTmuxPanes()
			require.NoError(t, err)
			paneID := run("display-message", "-p", "-t", "$1:1.1", "#{pane_id}")
			selected := listed.Directory["tmux-pane:code/"+paneID]
			require.Equal(t, "code/editor.1", selected.Name, "duplicate-pane picker names stay unchanged")
			c := NewConnector(model.Config{}, nil, nil, li, nil, nil, tm, nil, nil, nil)
			message, err := c.Connect(selected.Name, model.ConnectOpts{})
			require.NoError(t, err)
			require.Equal(t, "selected pane 1 in window 1", message)
			assert.Equal(t, intended, active("$1"), "intended user session/window/pane receives the selection")
			assert.Equal(t, bridgeBefore, active("$0"), "bridge active window and pane stay unchanged")
			assert.Equal(t, bridgePanesBefore, run("list-windows", "-t", "$0", "-F", "#{window_id} #{window_active} #{pane_id}"), "every bridge window retains its active pane")

			sh.sessionID = "$0"
			panes, err := tm.ListTmuxPanes()
			require.NoError(t, err)
			require.Len(t, panes, 3)
			for _, pane := range panes {
				require.Equal(t, "$0", pane.SessionID)
				require.Equal(t, "ssh/host/work", pane.SSHBridgeIdentity)
			}
			listed, err = li.ListTmuxPanes()
			require.NoError(t, err)
			require.Empty(t, listed.Directory, "marked bridge panes remain hidden")
			require.Empty(t, listed.OrderedIndex)
		})
	}
}
