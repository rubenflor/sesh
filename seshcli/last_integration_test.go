package seshcli

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

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Only client inventory and interactive handoff are injected. Session listing,
// ownership reads, and handoff target resolution use the private real server.
type lastPrivateShell struct {
	shell.Shell
	binary   string
	socket   string
	clients  []string
	handoffs [][]string
	resolved string
}

func (s *lastPrivateShell) run(args ...string) (string, error) {
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

func (s *lastPrivateShell) Cmd(_ string, args ...string) (string, error) {
	if args[0] == "attach-session" || args[0] == "switch-client" {
		s.handoffs = append(s.handoffs, append([]string(nil), args...))
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-t" {
				out, err := s.run("display-message", "-p", "-t", args[i+1], "#{session_id}")
				s.resolved = out
				return out, err
			}
		}
		return "", fmt.Errorf("handoff has no session target: %v", args)
	}
	return s.run(args...)
}

func (s *lastPrivateShell) CmdWithOutput(cmd string, args ...string) (string, error) {
	return s.Cmd(cmd, args...)
}

func (s *lastPrivateShell) CmdCapture(_ string, args ...string) (string, error) {
	return s.run(args...)
}

func (s *lastPrivateShell) CmdCaptureRaw(ctx context.Context, _ string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := []string{"-u", "TMUX", "-u", "TMUX_PANE", "-u", "SESH_CLIENT", "-u", model.SSHBridgeIdentityEnv, "SHELL=/bin/sh", s.binary, "-S", s.socket, "-f", "/dev/null"}
	return shell.NewShell(execwrap.NewExec(), nil).CmdCaptureRaw(ctx, "/usr/bin/env", stdoutLimit, stderrLimit, append(command, args...)...)
}

func (s *lastPrivateShell) ListCmd(_ string, args ...string) ([]string, error) {
	if args[0] == "list-clients" {
		return s.clients, nil
	}
	out, err := s.run(args...)
	return strings.Split(out, "\n"), err
}

func TestLastUsesVerifiedIDOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, name := range []string{"$0", "$1", "$2", "ordinary", "sesh-ssh-user"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMUX", "")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("SESH_CLIENT", "")
			dir, err := os.MkdirTemp(os.TempDir(), "sesh-last-")
			require.NoError(t, err)
			sh := &lastPrivateShell{binary: binary, socket: filepath.Join(dir, "socket"), clients: []string{
				"current-old::/dev/test1::$2::100",
				"other::/dev/test2::$3::999",
				"current-new::/dev/test3::$2::200",
			}}
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
			// Detached sessions have equal last-attach timestamps. Native tmux
			// name ordering puts the marked bridge first, then the current row,
			// selected user row, and oldest row; filtering must precede selection.
			for i, fixture := range []string{"!bridge", name, "!current", "~oldest"} {
				command := "printf 'USER_OUTPUT\\n'; exec sleep 120"
				if i == 0 {
					command = "printf 'BRIDGE_OUTPUT\\n'; exec sleep 120"
				}
				require.Equal(t, fmt.Sprintf("$%d", i), run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", fixture, "-c", dir, "--", "/bin/sh", "-c", command))
			}
			run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/host/work")
			run("rename-window", "-t", "$0:0", "active-bridge")
			run("new-window", "-d", "-t", "$0:", "-n", "inactive", "--", "/bin/sh", "-c", "exec sleep 120")
			require.Eventually(t, func() bool {
				return strings.Contains(run("capture-pane", "-p", "-t", "$0"), "BRIDGE_OUTPUT")
			}, 2*time.Second, 10*time.Millisecond)
			bridgeState := func() []string {
				return []string{
					run("display-message", "-p", "-t", "$0", "#{session_name} #{session_windows} #{window_id} #{window_name} #{pane_id} #{pane_pid}"),
					run("show-environment", "-t", "$0", model.SSHBridgeIdentityEnv),
					run("capture-pane", "-p", "-t", "$0"),
				}
			}
			before := bridgeState()
			configPath := filepath.Join(dir, "sesh.toml")
			require.NoError(t, os.WriteFile(configPath, nil, 0600))
			base := NewBaseDeps()
			base.Shell = sh
			deps, err := base.BuildAll(configPath)
			require.NoError(t, err)
			sessions, err := deps.Lister.List(lister.ListOptions{Tmux: true})
			require.NoError(t, err)
			require.Equal(t, []string{"tmux:!current", "tmux:" + name, "tmux:~oldest"}, sessions.OrderedIndex)
			selected, found := deps.Lister.GetLastTmuxSession()
			require.True(t, found)
			require.Equal(t, name, selected.Name)
			require.Equal(t, "$1", selected.TmuxSessionID)
			runLast := func() error {
				root := &cobra.Command{Use: "sesh", SilenceErrors: true, SilenceUsage: true}
				root.PersistentFlags().String("config", configPath, "")
				root.AddCommand(NewLastCommand(base))
				root.SetArgs([]string{"last"})
				return root.Execute()
			}
			for _, mode := range []struct {
				name, tmuxEnv, pane, client string
				want                        []string
			}{
				{"attach", "", "", "", []string{"attach-session", "-t", "$1"}},
				{"calling pane", sh.socket + ",1,2", "%2", "", []string{"switch-client", "-t", "$1"}},
				{"popup selects current client's most recent terminal", sh.socket + ",1,2", "", "", []string{"switch-client", "-c", "current-new", "-t", "$1"}},
				{"explicit client", sh.socket + ",1,2", "", "current-old", []string{"switch-client", "-c", "current-old", "-t", "$1"}},
				{"fallback selects most recent client", sh.socket + ",1,99", "", "", []string{"switch-client", "-c", "other", "-t", "$1"}},
			} {
				t.Run(mode.name, func(t *testing.T) {
					t.Setenv("TMUX", mode.tmuxEnv)
					t.Setenv("TMUX_PANE", mode.pane)
					t.Setenv("SESH_CLIENT", mode.client)
					sh.handoffs = nil
					require.NoError(t, runLast())
					require.Equal(t, "$1", sh.resolved)
					require.Equal(t, [][]string{mode.want}, sh.handoffs)
					require.Equal(t, before, bridgeState())
				})
			}
			if name == "ordinary" {
				require.NoError(t, os.WriteFile(configPath, []byte("blacklist = ['ordinary']\n"), 0600))
				sh.handoffs = nil
				require.NoError(t, runLast())
				require.Equal(t, "$3", sh.resolved)
				require.Equal(t, [][]string{{"attach-session", "-t", "$3"}}, sh.handoffs)
				require.NoError(t, os.WriteFile(configPath, nil, 0600))
			}
			run("kill-session", "-t", "$1")
			run("kill-session", "-t", "$3")
			sh.handoffs = nil
			require.EqualError(t, runLast(), "No last session found")
			require.Empty(t, sh.handoffs)
			require.Equal(t, before, bridgeState())
		})
	}
}
