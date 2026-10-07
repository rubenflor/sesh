package seshcli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/github"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/previewer"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/zoxide"
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

// Only terminal/client handoff is injected; ownership and operation targets
// are resolved by the same private tmux server.
func TestLocalOperationsUseVerifiedIDsOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, name := range []string{"$0", "$1", "ordinary", "sesh-ssh-user"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMUX", "")
			t.Setenv("SESH_CLIENT", "client")
			dir, err := os.MkdirTemp("/tmp", "sesh-local-target-")
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
			require.Equal(t, "$0", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "bridge", "printf 'BRIDGE_OUTPUT\\n'; sleep 60"))
			run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/host/work")
			require.Equal(t, "$1", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", name, "printf 'USER_OUTPUT\\n'; sleep 60"))
			require.Eventually(t, func() bool {
				return strings.Contains(run("capture-pane", "-p", "-t", "$0"), "BRIDGE_OUTPUT") &&
					strings.Contains(run("capture-pane", "-p", "-t", "$1"), "USER_OUTPUT")
			}, 2*time.Second, 10*time.Millisecond)

			focus := filepath.Join(dir, "focused")
			wrapper := filepath.Join(dir, "tmux")
			server := shell.Quote(binary) + " -S " + shell.Quote(socket) + " -f /dev/null"
			script := "#!/bin/sh\n" +
				"if [ \"$1\" = if-shell ]; then case \"$6\" in \"'attach-session' '-t' '\\$1'\"|\"'switch-client' '-c' 'client' '-t' '\\$1'\") " +
				server + " if-shell -F -t \"$4\" \"$5\" \"display-message -p -t '\\$1:' '#{session_id}'\" \"$7\" > " + shell.Quote(focus) + "; exit $?;; esac; fi\n" +
				"case \"$1\" in\n" +
				"list-clients) printf 'client::/dev/test::$0::100\\n'; exit 0;;\n" +
				"attach-session|switch-client) shift; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = -t ]; then target=$2; break; fi; shift; done; " +
				"[ \"$target\" = '$1' ] || exit 2; " +
				server + " display-message -p -t \"$target\" '#{session_id}' > " + shell.Quote(focus) + "; exit $?;;\n" +
				"esac\nexec " + server + " \"$@\"\n"
			require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
			config := filepath.Join(dir, "sesh.toml")
			require.NoError(t, os.WriteFile(config, []byte(fmt.Sprintf("tmux_command = %q\n", wrapper)), 0600))
			deps, err := NewBaseDeps().BuildAll(config)
			require.NoError(t, err)
			local, visible := deps.Lister.FindTmuxSession(name)
			require.True(t, visible)
			require.Equal(t, name, local.Name)
			_, visible = deps.Lister.FindTmuxSession("bridge")
			require.False(t, visible)
			f := focuser.NewMockFocuser(t)
			f.EXPECT().Activate("").Return(true, nil).Maybe()
			z := zoxide.NewMockZoxide(t)
			z.EXPECT().Add(local.Path).Return(nil).Times(4)
			c := connector.NewConnector(deps.Config, nil, nil, deps.Lister, nil, nil, deps.Tmux, z, nil, f)
			t.Run("connect", func(t *testing.T) {
				for _, inside := range []bool{false, true} {
					if inside {
						t.Setenv("TMUX", socket+",1,1")
					}
					for _, doSwitch := range []bool{false, true} {
						message, err := c.Connect(name, model.ConnectOpts{Switch: doSwitch})
						require.NoError(t, err)
						require.Contains(t, message, name)
						selected, err := os.ReadFile(focus)
						require.NoError(t, err)
						require.Equal(t, "$1\n", string(selected))
					}
				}
			})
			t.Run("preview", func(t *testing.T) {
				output, err := previewer.NewTmuxStrategy(deps.Lister, deps.Tmux).Execute(name)
				require.NoError(t, err)
				require.Contains(t, output, "USER_OUTPUT")
				require.NotContains(t, output, "BRIDGE_OUTPUT")
			})
			t.Run("rename", func(t *testing.T) {
				n := namer.NewMockNamer(t)
				n.EXPECT().Name(local.Path).Return("renamed-user", nil).Once()
				g := github.NewMockGithub(t)
				g.EXPECT().Issue(local.Path).Return(github.Issue{}, false, nil).Once()
				deps.Namer, deps.Github = n, g
				require.NoError(t, runEnrich(deps, []string{name}))
				require.Equal(t, "renamed-user", run("display-message", "-p", "-t", "$1", "#{session_name}"))
			})
			require.Equal(t, "bridge", run("display-message", "-p", "-t", "$0", "#{session_name}"))
			require.Equal(t, model.SSHBridgeIdentityEnv+"=ssh/host/work", run("show-environment", "-t", "$0", model.SSHBridgeIdentityEnv))
			require.Contains(t, run("capture-pane", "-p", "-t", "$0"), "BRIDGE_OUTPUT")
		})
	}
}

// Creation must resolve the literal name before delivering any startup keys.
// Only attach is injected; all other operations use the private real server.
func TestNewLocalSessionUsesVerifiedIDOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, name := range []string{"$0", "ordinary", "sesh-ssh-user"} {
		for _, mode := range []string{"startup", "command", "window", "rename"} {
			if mode == "rename" && name != "$0" {
				continue
			}
			t.Run(name+"/"+mode, func(t *testing.T) {
				t.Setenv("TMUX", "")
				dir, err := os.MkdirTemp("/tmp", "sesh-new-target-")
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
				require.Equal(t, "$0", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "bridge", "printf 'BRIDGE_OUTPUT\\n'; sleep 60"))
				run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/host/work")
				require.Eventually(t, func() bool {
					return strings.Contains(run("capture-pane", "-p", "-t", "$0"), "BRIDGE_OUTPUT")
				}, 2*time.Second, 10*time.Millisecond)
				bridgeOutput := run("capture-pane", "-p", "-t", "$0")
				bridgePane := run("display-message", "-p", "-t", "$0", "#{pane_id} #{pane_pid}")

				operations := filepath.Join(dir, "operations")
				focus := filepath.Join(dir, "focused")
				wrapper := filepath.Join(dir, "tmux")
				startupDone := filepath.Join(dir, "startup-done")
				windowDone := filepath.Join(dir, "window-done")
				keysSent := filepath.Join(dir, "rename-keys-sent")
				server := shell.Quote(binary) + " -S " + shell.Quote(socket) + " -f /dev/null"
				script := "#!/bin/sh\n" +
					"op=$1; target=; previous=; for arg in \"$@\"; do [ \"$previous\" != -t ] || target=$arg; previous=$arg; done\n" +
					"case \"$op\" in\n" +
					"send-keys|new-window|next-window|attach-session) printf '%s %s\\n' \"$op\" \"$target\" >> " + shell.Quote(operations) + ";;\nesac\n" +
					"if [ -f " + shell.Quote(keysSent) + " ] && { [ \"$op\" = list-sessions ] || [ \"$op\" = list-windows ] || [ \"$op\" = new-window ]; }; then i=0; while [ ! -f " + shell.Quote(startupDone) + " ] && [ \"$i\" -lt 300 ]; do sleep 0.01; i=$((i+1)); done; [ -f " + shell.Quote(startupDone) + " ] || exit 97; fi\n" +
					"if [ \"$op\" = send-keys ]; then " + server + " \"$@\"; status=$?; if [ \"$status\" -eq 0 ]; then case \"$*\" in *rename-session*) touch " + shell.Quote(keysSent) + ";; esac; fi; exit \"$status\"; fi\n" +
					"if [ \"$op\" = attach-session ]; then " + server + " display-message -p -t \"$target\" '#{session_id}' > " + shell.Quote(focus) + "; exit $?; fi\n" +
					"exec " + server + " \"$@\"\n"
				require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
				command := "touch " + shell.Quote(startupDone)
				if mode == "rename" {
					command = server + " rename-session -t " + shell.Quote("$1") + " renamed; " +
						server + " new-session -d -s " + shell.Quote("$0") + " -c " + shell.Quote(dir) + " " +
						shell.Quote("printf 'REPLACEMENT_OUTPUT\\n'; sleep 60") + "; touch " + shell.Quote(startupDone)
				}
				config := filepath.Join(dir, "sesh.toml")
				contents := fmt.Sprintf("tmux_command = %q\n[[session]]\nname = %q\npath = %q\nstartup_command = %q\nwindows = ['extra']\n[[window]]\nname = 'extra'\nstartup_script = %q\n", wrapper, name, dir, command, "touch "+shell.Quote(windowDone))
				require.NoError(t, os.WriteFile(config, []byte(contents), 0600))
				deps, err := NewBaseDeps().BuildAll(config)
				require.NoError(t, err)
				z := zoxide.NewMockZoxide(t)
				z.EXPECT().Add(dir).Return(nil).Once()
				c := connector.NewConnector(deps.Config, nil, nil, deps.Lister, nil, deps.Startup, deps.Tmux, z, nil, nil)
				if mode == "window" || mode == "rename" {
					_, err = c.ConnectWindow(model.WindowConnectOpts{Session: name, Name: "notes", Path: dir})
				} else {
					opts := model.ConnectOpts{}
					if mode == "command" {
						opts.Command = command
					}
					message, connectErr := c.Connect(name, opts)
					err = connectErr
					require.Contains(t, message, name)
				}
				require.NoError(t, err)
				if mode == "rename" {
					require.Equal(t, "renamed", run("display-message", "-p", "-t", "$1", "#{session_name}"))
					require.Equal(t, name, run("display-message", "-p", "-t", "$2", "#{session_name}"))
				} else {
					require.Equal(t, name, run("display-message", "-p", "-t", "$1", "#{session_name}"))
				}
				marker, err := deps.Tmux.GetSessionEnvironment("$1", model.SSHBridgeIdentityEnv)
				require.NoError(t, err)
				require.Empty(t, marker)
				selected, err := os.ReadFile(focus)
				require.NoError(t, err)
				require.Equal(t, "$1\n", string(selected))
				log, err := os.ReadFile(operations)
				require.NoError(t, err)
				require.Contains(t, string(log), "send-keys $1\n")
				require.Contains(t, string(log), "attach-session $1\n")
				for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
					fields := strings.Fields(line)
					require.Len(t, fields, 2)
					require.Contains(t, []string{"$1", "$1:"}, fields[1], "operation must preserve the verified ID: %s", line)
				}
				require.Eventually(t, func() bool { _, err := os.Stat(startupDone); return err == nil }, 2*time.Second, 10*time.Millisecond)
				if mode != "command" {
					require.Contains(t, string(log), "new-window $1:\n")
					require.Contains(t, string(log), "next-window $1:\n")
					require.Eventually(t, func() bool { _, err := os.Stat(windowDone); return err == nil }, 2*time.Second, 10*time.Millisecond)
				}
				if mode == "rename" {
					require.Equal(t, "1", run("display-message", "-p", "-t", "$2", "#{session_windows}"))
					require.NotContains(t, run("list-windows", "-t", "$2", "-F", "#{window_name}"), "notes")
					require.Contains(t, run("capture-pane", "-p", "-t", "$2"), "REPLACEMENT_OUTPUT")
					require.Contains(t, run("list-windows", "-t", "$1", "-F", "#{window_name}"), "notes")
				}
				require.Equal(t, "bridge", run("display-message", "-p", "-t", "$0", "#{session_name}"))
				require.Equal(t, bridgePane, run("display-message", "-p", "-t", "$0", "#{pane_id} #{pane_pid}"))
				require.Equal(t, model.SSHBridgeIdentityEnv+"=ssh/host/work", run("show-environment", "-t", "$0", model.SSHBridgeIdentityEnv))
				require.Equal(t, "1", run("display-message", "-p", "-t", "$0", "#{session_windows}"))
				require.Equal(t, bridgeOutput, run("capture-pane", "-p", "-t", "$0"))
			})
		}
	}
}
