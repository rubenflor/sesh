package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/require"
)

// Use a private local server and immediate-exit commands, never a real SSH host.
func TestInitialCommandExitLifecycle(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	// Keep the Unix socket path short enough on macOS.
	dir, err := os.MkdirTemp("", "sesh-tmux-")
	require.NoError(t, err)
	socket := filepath.Join(dir, "socket")
	run := func(args ...string) (string, error) {
		out, err := exec.Command(binary, append([]string{"-S", socket, "-f", "/dev/null"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() { _, _ = run("kill-server"); _ = os.RemoveAll(dir) })
	wrapper := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+shell.Quote(binary)+" -S "+shell.Quote(socket)+" -f /dev/null \"$@\"\n"), 0700))
	// An anchor keeps the isolated server available after a successful pane exit.
	out, err := run("new-session", "-d", "-s", "anchor", "sleep 60")
	require.NoError(t, err, out)
	tm := NewTmux(nil, shell.NewShell(execwrap.NewExec(), nil), wrapper)
	for _, tt := range []struct {
		name, command string
		dead          bool
	}{
		{"failure", "printf 'attach failed\n'; exit 23", true},
		{"success", "exit 0", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := tm.NewSessionWithCommand(model.TmuxSessionOpts{
				Name: tt.name, Command: tt.command, Environment: []string{"SESH_SSH_IDENTITY=ssh/host/0"},
				SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
				PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited: #{pane_dead_status}"}},
			})
			require.NoError(t, err)
			session := strings.Fields(ids)[0]
			if !tt.dead {
				require.Eventually(t, func() bool { _, err := run("has-session", "-t", session); return err != nil }, 3*time.Second, 10*time.Millisecond)
				return
			}
			require.Eventually(t, func() bool { dead, err := tm.IsPaneDead(session); return err == nil && dead }, 3*time.Second, 10*time.Millisecond)
			status, err := run("display-message", "-p", "-t", session+":", "#{pane_dead_status}")
			require.NoError(t, err)
			require.Equal(t, "23", status)
			captured, err := run("capture-pane", "-p", "-S", "-", "-t", session+":")
			require.NoError(t, err)
			require.Contains(t, captured, "attach failed")
			for _, option := range []string{"status", "detach-on-destroy"} {
				value, err := run("show-options", "-v", "-t", session, option)
				require.NoError(t, err)
				require.Equal(t, "off", value)
			}
			value, err := run("show-options", "-p", "-v", "-t", session+":", "remain-on-exit")
			require.NoError(t, err)
			require.Equal(t, "failed", value)
			// Bridge settings have not changed the server's global defaults.
			value, err = run("show-options", "-g", "-v", "status")
			require.NoError(t, err)
			require.Equal(t, "on", value)
			value, err = run("show-options", "-g", "-v", "detach-on-destroy")
			require.NoError(t, err)
			require.Equal(t, "on", value)
		})
	}
}
