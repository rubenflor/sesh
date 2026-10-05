package seshcli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// The server is real and private. Only interactive attach/client switching is
// replaced: the wrapper resolves each handed-off target on that same server.
func TestVerifiedSessionTargetsOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, name := range []string{"$0", "$1", "$2", "ordinary", "sesh-ssh-user"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMUX", "")
			t.Setenv("SESH_CLIENT", "client")
			dir, err := os.MkdirTemp("/tmp", "sesh-target-")
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
			for i := 0; i < 3; i++ {
				require.Equal(t, fmt.Sprintf("$%d", i), run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", fmt.Sprintf("anchor%d", i), "sleep 60"))
			}
			collisionID := "$1"
			if strings.HasPrefix(name, "$") {
				collisionID = name
			}
			run("set-environment", "-t", collisionID, model.SSHBridgeIdentityEnv, "ssh/host/work")
			run("rename-window", "-t", collisionID+":0", "hidden")
			localID := run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", name, "-n", "local", "sleep 60")
			require.Equal(t, "$3", localID)
			focus := filepath.Join(dir, "focused")
			wrapper := filepath.Join(dir, "tmux")
			server := shell.Quote(binary) + " -S " + shell.Quote(socket) + " -f /dev/null"
			script := "#!/bin/sh\n" +
				"case \"$1\" in\n" +
				"list-clients) printf 'client::/dev/test::$0::100\\n'; exit 0;;\n" +
				"attach-session|switch-client) shift; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = -t ]; then target=$2; break; fi; shift; done; " +
				server + " display-message -p -t \"$target\" '#{session_id}' > " + shell.Quote(focus) + "; exit $?;;\n" +
				"esac\nexec " + server + " \"$@\"\n"
			require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
			config := filepath.Join(dir, "sesh.toml")
			require.NoError(t, os.WriteFile(config, []byte(fmt.Sprintf("tmux_command = %q\n[ssh]\nhosts = ['host']\n", wrapper)), 0600))
			base := NewBaseDeps()
			deps, err := base.BuildAll(config)
			require.NoError(t, err)

			for _, jsonOutput := range []bool{false, true} {
				root := &cobra.Command{Use: "sesh"}
				root.PersistentFlags().String("config", config, "")
				root.AddCommand(NewWindowCommand(base))
				args := []string{"window", "list", "--target", name}
				if jsonOutput {
					args = append(args, "--json")
				}
				root.SetArgs(args)
				out := captureWindowStdout(t, root.Execute)
				if jsonOutput {
					var windows []model.TmuxWindow
					require.NoError(t, json.Unmarshal([]byte(out), &windows))
					require.Len(t, windows, 1)
					require.Equal(t, "local", windows[0].Name)
				} else {
					require.Equal(t, "local\n", out)
				}
			}
			// Create and reuse a window using a literal ID-shaped local name.
			for i := 0; i < 2; i++ {
				_, err := deps.Connector.ConnectWindow(model.WindowConnectOpts{Session: name, Name: "notes", Path: dir, Command: "sleep 60", Background: true})
				require.NoError(t, err)
			}
			require.Contains(t, run("list-windows", "-t", localID, "-F", "#{window_name}"), "notes")
			require.Equal(t, "hidden", run("list-windows", "-t", collisionID, "-F", "#{window_name}"))

			// Now reuse a marked bridge renamed to an unrelated session's ID.
			run("set-environment", "-u", "-t", collisionID, model.SSHBridgeIdentityEnv)
			run("rename-session", "-t", localID, "temporary-bridge")
			run("set-environment", "-t", localID, model.SSHBridgeIdentityEnv, "ssh/host/work")
			run("rename-session", "-t", localID, name)
			f := focuser.NewMockFocuser(t)
			f.EXPECT().Activate("").Return(true, nil).Maybe()
			c := connector.NewConnector(deps.Config, nil, nil, nil, nil, nil, deps.Tmux, nil, nil, f)
			for _, inside := range []bool{false, true} {
				if inside {
					t.Setenv("TMUX", socket+",1,3")
				}
				for _, doSwitch := range []bool{false, true} {
					message, err := c.Connect("ssh/host/work", model.ConnectOpts{Switch: doSwitch})
					require.NoError(t, err)
					require.Equal(t, "connected to SSH session: ssh/host/work", message)
					selected, err := os.ReadFile(focus)
					require.NoError(t, err)
					require.Equal(t, localID+"\n", string(selected))
				}
			}
		})
	}
}

func captureWindowStdout(t *testing.T, run func() error) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout")
	require.NoError(t, err)
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous; _ = file.Close() }()
	err = run()
	os.Stdout = previous
	require.NoError(t, err)
	out, err := os.ReadFile(file.Name())
	require.NoError(t, err)
	return string(out)
}
