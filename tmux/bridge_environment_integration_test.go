package tmux_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

func TestBridgeIdentityUsesOnlyOwningSessionEnvironment(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	// Keep the private Unix socket path short enough for macOS.
	dir, err := os.MkdirTemp("/tmp", "sesh-env-")
	require.NoError(t, err)
	socket := filepath.Join(dir, "socket")
	run := func(args ...string) (string, error) {
		out, err := exec.Command(binary, append([]string{"-S", socket, "-f", "/dev/null"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() { _, _ = run("kill-server"); _ = os.RemoveAll(dir) })
	command := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		require.NoError(t, err, out)
		return out
	}
	normalID := command("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "sesh-ssh-user", "sleep 60")
	command("split-window", "-d", "-t", normalID+":", "sleep 60")
	// Grouped sessions share pane IDs, but their session environments differ.
	bridgeID := command("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "bridge", "-t", normalID)
	require.Equal(t,
		command("list-panes", "-s", "-t", normalID, "-F", "#{pane_id}"),
		command("list-panes", "-s", "-t", bridgeID, "-F", "#{pane_id}"),
		"grouped sessions share the same panes")
	command("set-environment", "-u", "-t", normalID, model.SSHBridgeIdentityEnv)
	command("set-environment", "-t", bridgeID, model.SSHBridgeIdentityEnv, "ssh/session/work")
	command("set-environment", "-g", model.SSHBridgeIdentityEnv, "ssh/global/work")
	require.Equal(t, model.SSHBridgeIdentityEnv+"=ssh/global/work", command("show-environment", "-g", model.SSHBridgeIdentityEnv))
	_, err = run("show-environment", "-t", normalID, "--", model.SSHBridgeIdentityEnv)
	require.Error(t, err, "the normal session has no session-local marker")

	newTmux := func(target string) tmux.Tmux {
		t.Helper()
		wrapper := filepath.Join(t.TempDir(), "tmux")
		script := "#!/bin/sh\nif [ \"$1\" = -u ] && [ \"$2\" = list-panes ]; then\n  exec " + shell.Quote(binary) + " -S " + shell.Quote(socket) + " -f /dev/null \"$@\" -t " + shell.Quote(target) + "\nfi\nexec " + shell.Quote(binary) + " -S " + shell.Quote(socket) + " -f /dev/null \"$@\"\n"
		require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
		return tmux.NewTmux(nil, shell.NewShell(execwrap.NewExec(), nil), wrapper)
	}

	t.Run("sessions", func(t *testing.T) {
		tm := newTmux(normalID)
		identity, err := tm.GetSessionEnvironment(normalID, model.SSHBridgeIdentityEnv)
		require.NoError(t, err)
		require.Empty(t, identity)
		sessions, err := tm.ListSessions()
		require.NoError(t, err)
		require.Len(t, sessions, 2)
		for _, session := range sessions {
			if session.ID == normalID {
				require.Empty(t, session.SSHBridgeIdentity)
			} else {
				require.Equal(t, bridgeID, session.ID)
				require.Equal(t, "ssh/session/work", session.SSHBridgeIdentity)
			}
		}
		public := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
		listed, err := public.List(lister.ListOptions{Tmux: true})
		require.NoError(t, err)
		require.Equal(t, []string{"tmux:sesh-ssh-user"}, listed.OrderedIndex)
		require.Contains(t, listed.Directory, "tmux:sesh-ssh-user")
		require.NotContains(t, listed.Directory, "tmux:bridge")
	})
	for _, tc := range []struct {
		name, sessionID, identity string
		visible                   int
	}{
		{"normal panes", normalID, "", 2},
		{"bridge panes", bridgeID, "ssh/session/work", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tm := newTmux(tc.sessionID)
			panes, err := tm.ListTmuxPanes()
			require.NoError(t, err)
			require.Len(t, panes, 2)
			for _, pane := range panes {
				require.Equal(t, tc.sessionID, pane.SessionID)
				require.Equal(t, tc.identity, pane.SSHBridgeIdentity)
			}
			public := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			listed, err := public.ListTmuxPanes()
			require.NoError(t, err)
			require.Len(t, listed.OrderedIndex, tc.visible)
			require.Len(t, listed.Directory, tc.visible)
			for _, pane := range panes {
				key := "tmux-pane:" + pane.WindowName + "/" + pane.PaneID
				if tc.visible > 0 {
					require.Contains(t, listed.OrderedIndex, key)
					require.Contains(t, listed.Directory, key)
				} else {
					require.NotContains(t, listed.OrderedIndex, key)
					require.NotContains(t, listed.Directory, key)
				}
			}
		})
	}
}
