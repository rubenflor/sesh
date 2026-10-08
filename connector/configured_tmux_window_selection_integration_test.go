//go:build unix

package connector

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Honor the executable supplied by production, adding only the existing
// runner's deadline. Unlike panePrivateShell, this must not override its socket.
type configuredWindowShell struct{ shell.Shell }

func (s *configuredWindowShell) CmdCapture(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := execwrap.NewExec().RunContext(ctx, bin, args...)
	out := strings.TrimSuffix(string(result.Stdout), "\n")
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", bin, err, result.Stderr)
	}
	return out, nil
}

func (s *configuredWindowShell) Cmd(bin string, args ...string) (string, error) {
	return s.CmdCapture(bin, args...)
}

func (s *configuredWindowShell) ListCmd(bin string, args ...string) ([]string, error) {
	out, err := s.CmdCapture(bin, args...)
	return strings.Split(out, "\n"), err
}

func TestConfiguredTmuxWindowSelectionOnPrivateServers(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	parent, err := os.MkdirTemp("/tmp", "sesh-select-")
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	var fixtures []*bridgePrivateFixture
	t.Cleanup(func() {
		for _, fixture := range fixtures {
			if !fixture.removed {
				t.Errorf("retaining %s: private server exit is unverified", root)
				return
			}
			t.Logf("private server stopped: %s (%s; socket and PID verified)", fixture.socket, fixture.starts[0].State)
		}
		require.NoError(t, os.RemoveAll(root))
	})
	t.Setenv("TMPDIR", root)
	for _, variable := range []string{"TMUX", "TMUX_PANE", "SESH_CLIENT", model.SSHBridgeIdentityEnv, "ENV", "BASH_ENV"} {
		t.Setenv(variable, "")
	}
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", "/usr/bin:/bin")
	command := func(t *testing.T, sh *panePrivateShell, args ...string) string {
		t.Helper()
		out, err := sh.run(args...)
		require.NoError(t, err)
		return out
	}
	start := func(label string) (*bridgePrivateFixture, *panePrivateShell) {
		t.Helper()
		fixture, sh := newBridgePrivateFixture(t, binary)
		fixtures = append(fixtures, fixture)
		sh.socket = filepath.Join(fixture.dir, label+".socket")
		fixture.socket = sh.socket
		require.NoError(t, fixture.startAnchor())
		command(t, sh, "set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/"+label)
		require.Equal(t, "$1 @1", command(t, sh, "new-session", "-d", "-P", "-F", "#{session_id} #{window_id}", "-s", label+"_user", "-n", label+"_initial", "-c", fixture.dir, "--", "exec sleep 120"))
		command(t, sh, "set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, "")
		for i := 2; i <= 6; i++ {
			require.Equal(t, fmt.Sprintf("@%d", i), command(t, sh, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$0:", "-n", "filler", "--", "exec sleep 120"))
		}
		marker := label + "_WINDOW_OUTPUT"
		require.Equal(t, "@7", command(t, sh, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$1:4", "-n", label+"_notes", "-c", fixture.dir, "--", "printf '"+marker+"\\n'; exec /bin/cat"))
		require.Eventually(t, func() bool {
			out, err := sh.run("capture-pane", "-p", "-t", "$1:@7")
			return err == nil && strings.Contains(out, marker) && command(t, sh, "display-message", "-p", "-t", "$1:@7", "#{pane_current_command}") == "cat"
		}, 3*time.Second, 10*time.Millisecond, "the private pane must be ready before snapshotting")
		command(t, sh, "select-window", "-t", "$1:@1")
		return fixture, sh
	}
	a, serverA := start("A")
	b, serverB := start("B")
	require.NotEqual(t, a.starts[0].Generation, b.starts[0].Generation)
	configuredLog, defaultLog := filepath.Join(root, "configured.argv"), filepath.Join(root, "default.argv")
	wrapper := func(path, socket, log string) string {
		t.Helper()
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + shell.Quote(log) + "\nexec " + shell.Quote(binary) + " -S " + shell.Quote(socket) + " -f /dev/null \"$@\"\n"
		require.NoError(t, os.WriteFile(path, []byte(script), 0700))
		return path
	}
	configured := wrapper(filepath.Join(root, "configured wrapper"), a.socket, configuredLog)
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(binDir, 0700))
	wrapper(filepath.Join(binDir, "tmux"), b.socket, defaultLog)
	t.Setenv("PATH", binDir+":/usr/bin:/bin")
	tm := tmux.NewTmux(oswrap.NewOs(), &configuredWindowShell{}, configured)
	live, err := tmux.VerifyLocalSession(tm, "$1", a.starts[0].Generation)
	require.NoError(t, err)
	windows, err := tm.ListWindows(live.ID, live.ServerGeneration)
	require.NoError(t, err)
	var selected *model.TmuxWindow
	for _, window := range windows {
		if window.ID == "@7" {
			selected = window
		}
	}
	require.NotNil(t, selected)
	require.Equal(t, "A_notes", selected.Name)
	require.Equal(t, live.ServerGeneration, selected.ServerGeneration)
	target := live.ID + ":" + selected.ID
	require.Equal(t, "$1:@7", target)
	require.Equal(t, "$1 @7 B_notes", command(t, serverB, "display-message", "-p", "-t", target, "#{session_id} #{window_id} #{window_name}"), "the same IDs identify a different window on B")
	state := func(t *testing.T, sh *panePrivateShell, session string) []string {
		t.Helper()
		sessions := []string{session}
		if session == "" {
			sessions = strings.Fields(command(t, sh, "list-sessions", "-F", "#{session_id}"))
		}
		result := []string{command(t, sh, "show-options", "-g"), command(t, sh, "show-environment", "-g")}
		for _, id := range sessions {
			result = append(result,
				command(t, sh, "display-message", "-p", "-t", id+":", "#{session_id} #{session_name} #{session_path} #{session_windows} #{session_attached}"),
				command(t, sh, "show-options", "-t", id), command(t, sh, "show-environment", "-t", id),
				command(t, sh, "list-windows", "-t", id+":", "-F", "#{window_id} #{window_index} #{window_name} #{window_active} #{pane_id}"),
				command(t, sh, "list-panes", "-s", "-t", id+":", "-F", "#{pane_id} #{pane_pid} #{pane_dead} #{pane_current_command} #{pane_current_path}"))
			for _, pane := range strings.Fields(command(t, sh, "list-panes", "-s", "-t", id+":", "-F", "#{pane_id}")) {
				result = append(result, command(t, sh, "capture-pane", "-p", "-t", pane))
			}
		}
		return result
	}
	readLog := func(t *testing.T, path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(data)
	}
	reset := func(t *testing.T) {
		t.Helper()
		command(t, serverA, "select-window", "-t", "$1:@1")
		command(t, serverB, "select-window", "-t", "$1:@1")
		for _, log := range []string{configuredLog, defaultLog} {
			require.NoError(t, os.WriteFile(log, nil, 0600))
		}
	}
	for _, guarded := range []bool{false, true} {
		t.Run(map[bool]string{false: "listed identity", true: "generation guarded identity"}[guarded], func(t *testing.T) {
			reset(t)
			beforeB, bridgeBefore := state(t, serverB, ""), state(t, serverA, "$0")
			var generation []string
			if guarded {
				generation = []string{selected.ServerGeneration}
			}
			out, err := tm.SelectWindow(target, generation...)
			assert.NoError(t, err)
			assert.Empty(t, out)
			activeA := command(t, serverA, "display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}")
			activeB := command(t, serverB, "display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}")
			t.Logf("after selection: A=%s, B=%s", activeA, activeB)
			assert.Equal(t, "$1 @7", activeA)
			assert.Equal(t, "$1 @1", activeB)
			assert.True(t, slices.Equal(beforeB, state(t, serverB, "")), "B's sessions, windows, panes, options, environment and output must remain unchanged")
			assert.True(t, slices.Equal(bridgeBefore, state(t, serverA, "$0")), "A's marked bridge must remain unchanged")
			assert.Empty(t, readLog(t, defaultLog), "bare tmux must never be invoked")
			argv := readLog(t, configuredLog)
			if guarded {
				assert.True(t, strings.HasPrefix(argv, "if-shell\n-F\n-t\n$1:@7\n"))
				assert.Contains(t, argv, "'select-window' '-t' '$1:@7'\nif-shell\n")
			} else {
				assert.Equal(t, "select-window\n-t\n$1:@7\n", argv)
			}
			t.Logf("configured-wrapper argv: %q", argv)
		})
	}
	t.Run("connector detached window handoff", func(t *testing.T) {
		reset(t)
		beforeB, bridgeBefore := state(t, serverB, ""), state(t, serverA, "$0")
		li := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
		zo := zoxide.NewMockZoxide(t)
		zo.EXPECT().Add(a.dir).Return(nil).Once()
		focus := focuser.NewMockFocuser(t)
		focus.EXPECT().Activate("").Return(false, nil).Once()
		c := NewConnector(model.Config{}, nil, nil, li, nil, nil, tm, zo, nil, focus)
		label, err := c.ConnectWindow(model.WindowConnectOpts{Name: selected.Name, Session: live.Name, Path: a.dir, Switch: true})
		require.NoError(t, err)
		require.Equal(t, "A_user:4", label)
		require.Equal(t, "$1 @7", command(t, serverA, "display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}"))
		require.True(t, slices.Equal(beforeB, state(t, serverB, "")))
		require.True(t, slices.Equal(bridgeBefore, state(t, serverA, "$0")))
		require.Empty(t, readLog(t, defaultLog))
		require.Equal(t, 2, strings.Count(readLog(t, configuredLog), "'select-window' '-t' '$1:@7'"), "selection and the detached no-client handoff must both retain the configured namespace")
	})
	t.Run("default configured executable", func(t *testing.T) {
		reset(t)
		beforeB := state(t, serverB, "")
		defaultA := filepath.Join(root, "default-a")
		require.NoError(t, os.Mkdir(defaultA, 0700))
		wrapper(filepath.Join(defaultA, "tmux"), a.socket, configuredLog)
		t.Setenv("PATH", defaultA+":/usr/bin:/bin")
		_, err := tmux.NewTmux(nil, &configuredWindowShell{}, "").SelectWindow(target, selected.ServerGeneration)
		require.NoError(t, err)
		require.Equal(t, "$1 @7", command(t, serverA, "display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}"))
		require.True(t, slices.Equal(beforeB, state(t, serverB, "")))
		require.Empty(t, readLog(t, defaultLog))
	})
	failing := filepath.Join(root, "failing wrapper")
	require.NoError(t, os.WriteFile(failing, []byte("#!/bin/sh\nprintf 'configured failure\\n'\nexit 23\n"), 0700))
	unavailable := wrapper(filepath.Join(root, "unavailable wrapper"), filepath.Join(root, "absent.socket"), configuredLog)
	for _, test := range []struct{ name, bin string }{
		{"failing wrapper", failing}, {"missing executable", filepath.Join(root, "missing")}, {"unavailable server", unavailable},
	} {
		t.Run(test.name+" has no fallback", func(t *testing.T) {
			reset(t)
			beforeA, beforeB := state(t, serverA, ""), state(t, serverB, "")
			out, err := tmux.NewTmux(nil, &configuredWindowShell{}, test.bin).SelectWindow(target, selected.ServerGeneration)
			require.Error(t, err)
			if test.name == "failing wrapper" {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, 23, exitErr.ExitCode())
				require.Equal(t, "configured failure", out)
			}
			require.Empty(t, readLog(t, defaultLog), "a configured failure must never invoke the default executable")
			require.True(t, slices.Equal(beforeA, state(t, serverA, "")))
			require.True(t, slices.Equal(beforeB, state(t, serverB, "")))
		})
	}
}
