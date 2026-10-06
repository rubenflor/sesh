package tmux_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

func TestFocusedPathPrivateTmuxUsesOwnershipAndVerifiedIDs(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "sesh-focus-")
	require.NoError(t, err)
	socket := filepath.Join(dir, "socket")
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(binary, append([]string{"-S", socket, "-f", "/dev/null"}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "-S", socket, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	for _, fixture := range []struct {
		name   string
		marked bool
	}{
		{"$1", false}, {"sesh-ssh-user", false}, {"renamed-bridge", true}, {"$0", true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			cwd := t.TempDir()
			id := run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", fixture.name, "-c", cwd, "sleep 60")
			if fixture.marked {
				run("set-environment", "-t", id, model.SSHBridgeIdentityEnv, "ssh/host/work")
			}
			wrapper := filepath.Join(t.TempDir(), "tmux")
			// Resolve an explicitly focused client among several, then use real
			// tmux commands for the ID, ownership marker and path reads.
			script := "#!/bin/sh\nif [ \"$1\" = list-clients ]; then\n" +
				" printf '%s\\n' 'other::/dev/tty1::$999::999' " + shell.Quote("chosen::/dev/tty2::"+id+"::100") + "\n exit 0\nfi\n" +
				"exec " + shell.Quote(binary) + " -S " + shell.Quote(socket) + " -f /dev/null \"$@\"\n"
			require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
			mOs := oswrap.NewMockOs(t)
			mOs.EXPECT().Getenv("SESH_CLIENT").Return("chosen")
			tm := tmux.NewTmux(mOs, shell.NewShell(execwrap.NewExec(), nil), wrapper)
			path, err := tm.FocusedSessionPath()
			if fixture.marked {
				require.ErrorContains(t, err, "focus a local session")
				require.Empty(t, path)
			} else {
				require.NoError(t, err)
				require.Equal(t, cwd, path)
			}
		})
	}
}
