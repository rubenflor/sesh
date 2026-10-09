//go:build unix

package seshcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/github"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/previewer"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// All native commands, including SelectWindow's deferred bare-bin path, are
// confined to this exact socket. Only interactive client handoff is replaced
// with a native target query inside the original generation guard.
type existingPrivateTmux struct {
	shell.Shell
	t                   *testing.T
	binary, dir, socket string
	pids                []int
	beforeOperation     func(string)
	afterInventory      func()
	afterCapture        func(int)
	captures            int
	focused             []string
	attached            bool
}

func newExistingPrivateTmux(t *testing.T) *existingPrivateTmux {
	t.Helper()
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "sesh-existing-")
	require.NoError(t, err)
	dir, err = filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	s := &existingPrivateTmux{t: t, binary: binary, dir: dir, socket: filepath.Join(dir, "socket")}
	t.Cleanup(func() {
		if err := s.stop(); err != nil {
			t.Errorf("retaining private tmux fixture %s: %v", s.dir, err)
			return
		}
		if err := os.RemoveAll(s.dir); err != nil {
			t.Error(err)
		}
	})
	for _, variable := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		path := filepath.Join(dir, strings.ToLower(variable))
		require.NoError(t, os.Mkdir(path, 0700))
		t.Setenv(variable, path)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("SESH_CLIENT", "private-client")
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")
	s.start()
	for _, root := range []string{"original", "replacement"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, root), 0700))
	}
	s.newSession("$1", "$0", "original", "ORIGINAL_OUTPUT")
	s.must("new-window", "-d", "-t", "$1:", "-n", "notes", "-c", filepath.Join(dir, "original"), "--", "/bin/sh", "-c", "printf 'ORIGINAL_NOTES\n'; exec sleep 120")
	s.ready("$1:1", "ORIGINAL_NOTES")
	return s
}

func (s *existingPrivateTmux) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.binary, append([]string{"-u", "-S", s.socket, "-f", "/dev/null"}, args...)...)
	cmd.WaitDelay = 250 * time.Millisecond
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		switch name {
		case "TMUX", "TMUX_PANE", "SESH_CLIENT", "SHELL", model.SSHBridgeIdentityEnv:
		default:
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "SHELL=/bin/sh", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := strings.TrimSuffix(stdout.String(), "\n")
	if err != nil {
		return out, fmt.Errorf("private tmux %v: %w: %s", args, err, stderr.String())
	}
	return out, nil
}

func (s *existingPrivateTmux) must(args ...string) string {
	s.t.Helper()
	out, err := s.run(args...)
	require.NoError(s.t, err)
	return out
}

func (s *existingPrivateTmux) start() {
	s.t.Helper()
	require.Equal(s.t, "$0", s.must("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "bridge", "-n", "bridge", "-c", s.dir, "--", "/bin/sh", "-c", "printf 'BRIDGE_OUTPUT\n'; exec sleep 120"))
	generation := s.must("display-message", "-p", "-t", "$0:", "#{pid}:#{start_time}:#{socket_path}")
	pid, err := strconv.Atoi(strings.SplitN(generation, ":", 3)[0])
	require.NoError(s.t, err)
	require.Positive(s.t, pid)
	s.pids = append(s.pids, pid)
	s.must("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/bridge")
	s.ready("$0", "BRIDGE_OUTPUT")
}

func (s *existingPrivateTmux) stop() error {
	_, killErr := s.run("kill-server")
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := s.run("display-message", "-p", "#{pid}")
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 ||
				(!strings.HasSuffix(err.Error(), "no server running on "+s.socket+"\n") && !strings.HasSuffix(err.Error(), "error connecting to "+s.socket+" (No such file or directory)\n")) {
				return fmt.Errorf("private socket shutdown unverified: %w (kill: %v)", err, killErr)
			}
			allExited := true
			for _, pid := range s.pids {
				if syscall.Kill(pid, 0) != syscall.ESRCH {
					allExited = false
				}
			}
			if allExited {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("private server shutdown timed out on %s", s.socket)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *existingPrivateTmux) newSession(id, name, root, output string) {
	s.t.Helper()
	require.Equal(s.t, id, s.must("new-session", "-d", "-P", "-F", "#{session_id}", "-s", name, "-n", root,
		"-c", filepath.Join(s.dir, root), "--", "/bin/sh", "-c", "printf '"+output+"\n'; exec sleep 120"))
	s.ready(id, output)
}

func (s *existingPrivateTmux) ready(id, output string) {
	s.t.Helper()
	target := id
	if !strings.Contains(target, ":") {
		target += ":"
	}
	require.Eventually(s.t, func() bool { return strings.Contains(s.must("capture-pane", "-p", "-t", target), output) }, 2*time.Second, 10*time.Millisecond)
}

func (s *existingPrivateTmux) state(id string) []string {
	return []string{
		s.must("display-message", "-p", "-t", id+":", "#{session_id} #{session_name} #{session_path} #{window_id} #{pane_id} #{pane_pid}"),
		s.must("list-windows", "-t", id+":", "-F", "#{window_id} #{window_index} #{window_name} #{window_active} #{pane_id}"),
		s.must("show-environment", "-t", id),
		s.must("capture-pane", "-p", "-t", id+":"),
	}
}

func (s *existingPrivateTmux) command(args ...string) (string, error) {
	args = append([]string(nil), args...)
	op := args[0]
	guarded := op == "if-shell" && len(args) == 7
	if guarded {
		op = strings.Trim(strings.Fields(args[5])[0], "'")
	}
	if s.beforeOperation != nil {
		s.beforeOperation(op)
	}
	focus := op == "attach-session" || op == "switch-client"
	if focus {
		var target string
		if guarded {
			_, rest, found := strings.Cut(args[5], "'-t' '")
			require.True(s.t, found)
			target, _, found = strings.Cut(rest, "'")
			require.True(s.t, found)
			args[5] = "display-message -p -t " + shell.Quote(target+":") + " '#{session_id}'"
		} else {
			for i, arg := range args {
				if arg == "-t" && i+1 < len(args) {
					target = args[i+1]
					break
				}
			}
			args = []string{"display-message", "-p", "-t", target + ":", "#{session_id}"}
		}
	} else if op == "display-message" && len(args) == 3 {
		args = append([]string{"display-message", "-t", "$1:"}, args[1:]...)
	}
	out, err := s.run(args...)
	if err == nil && focus {
		s.focused = append(s.focused, out)
	}
	if err == nil && op == "capture-pane" {
		s.captures++
		if s.afterCapture != nil {
			s.afterCapture(s.captures)
		}
	}
	return out, err
}

func (s *existingPrivateTmux) Cmd(_ string, args ...string) (string, error) {
	return s.command(args...)
}

func (s *existingPrivateTmux) CmdCapture(_ string, args ...string) (string, error) {
	return s.command(args...)
}

func (s *existingPrivateTmux) CmdCaptureRaw(ctx context.Context, _ string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
	inventory := args[0] == "-u" && args[1] == "list-sessions"
	if inventory && s.attached {
		args = append([]string(nil), args...)
		args[3] = strings.ReplaceAll(args[3], "#{session_attached}", "#{?#{==:#{session_id},$1},1,#{session_attached}}")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := []string{"-u", "TMUX", "-u", "TMUX_PANE", "-u", "SESH_CLIENT", "-u", model.SSHBridgeIdentityEnv, "SHELL=/bin/sh", s.binary, "-S", s.socket, "-f", "/dev/null"}
	result, err := shell.NewShell(execwrap.NewExec(), nil).CmdCaptureRaw(ctx, "/usr/bin/env", stdoutLimit, stderrLimit, append(command, args...)...)
	if err == nil && inventory && s.afterInventory != nil {
		after := s.afterInventory
		s.afterInventory = nil
		after()
	}
	return result, err
}

func (s *existingPrivateTmux) CmdWithOutput(_ string, args ...string) (string, error) {
	return s.command(args...)
}

func (s *existingPrivateTmux) ListCmd(_ string, args ...string) ([]string, error) {
	if args[0] == "list-clients" {
		return []string{"private-client::/dev/private::$0::100"}, nil
	}
	out, err := s.command(args...)
	return strings.Split(out, "\n"), err
}

func (s *existingPrivateTmux) commandRoot(cmd *cobra.Command) *cobra.Command {
	config := filepath.Join(s.dir, "sesh.toml")
	require.NoError(s.t, os.WriteFile(config, []byte("cache = false\n"), 0600))
	root := &cobra.Command{Use: "sesh", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().String("config", config, "")
	root.AddCommand(cmd)
	return root
}

func TestExistingSessionIdentityRaceOnPrivateTmux(t *testing.T) {
	for _, action := range []string{
		"attach", "switch", "detached switch", "enrich named", "enrich attached", "preview", "watched preview",
		"existing window", "default window", "create window", "default create", "implicit listing", "explicit listing",
	} {
		t.Run(action, func(t *testing.T) {
			s := newExistingPrivateTmux(t)
			if action == "switch" || strings.HasPrefix(action, "default") || action == "implicit listing" {
				s.attached = true
				t.Setenv("TMUX", s.socket+",1,1")
			}
			tm := tmux.NewTmux(NewBaseDeps().Os, s, "private-tmux")
			liveLister := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			selected, found := liveLister.FindTmuxSession("$0")
			require.True(t, found)
			require.Equal(t, "$1", selected.TmuxSessionID)
			require.NotEmpty(t, selected.TmuxServerGeneration)
			bridgeBefore := s.state("$0")
			var replacementBefore []string
			change := func() {
				s.must("rename-session", "-t", "$1:", "renamed")
				s.newSession("$2", "$0", "replacement", "REPLACEMENT_OUTPUT")
				// Exact session environments must ignore this global marker.
				s.must("set-environment", "-g", model.SSHBridgeIdentityEnv, "ssh/global/ignored")
				replacementBefore = s.state("$2")
			}
			li := lister.NewMockLister(t)
			if action != "watched preview" && !strings.HasSuffix(action, "listing") {
				if action == "enrich attached" || strings.HasPrefix(action, "default") {
					li.EXPECT().GetAttachedTmuxSession().RunAndReturn(func() (model.SeshSession, bool) {
						if action != "enrich attached" {
							change()
						}
						return selected, true
					}).Once()
				} else {
					li.EXPECT().FindTmuxSession("$0").RunAndReturn(func(string) (model.SeshSession, bool) {
						if action != "enrich named" {
							change()
						}
						return selected, true
					}).Once()
				}
			}
			z := zoxide.NewMockZoxide(t)
			z.EXPECT().Add(selected.Path).Return(nil).Maybe()
			f := focuser.NewMockFocuser(t)
			f.EXPECT().Activate("").Return(true, nil).Maybe()
			c := connector.NewConnector(model.Config{}, nil, nil, li, nil, nil, tm, z, nil, f)
			switch action {
			case "attach", "switch", "detached switch":
				message, err := c.Connect("$0", model.ConnectOpts{Switch: action == "detached switch"})
				require.NoError(t, err)
				require.Contains(t, message, "$0")
				require.Equal(t, []string{"$1"}, s.focused)
			case "enrich named", "enrich attached":
				n := namer.NewMockNamer(t)
				n.EXPECT().Name(selected.Path).RunAndReturn(func(string) (string, error) { change(); return "enriched", nil }).Once()
				g := github.NewMockGithub(t)
				g.EXPECT().Issue(selected.Path).Return(github.Issue{Title: "original issue"}, true, nil).Once()
				deps := &Deps{Lister: li, Tmux: tm, Namer: n, BaseDeps: BaseDeps{Github: g}}
				var args []string
				if action == "enrich named" {
					args = []string{"$0"}
				}
				require.NoError(t, runEnrich(deps, args))
				require.Equal(t, "enriched — original issue", s.must("display-message", "-p", "-t", "$1:", "#{session_name}"))
			case "preview":
				output, err := previewer.NewTmuxStrategy(li, tm).Execute("$0")
				require.NoError(t, err)
				require.Contains(t, output, "ORIGINAL_OUTPUT")
				require.NotContains(t, output, "REPLACEMENT_OUTPUT")
				require.NotContains(t, output, "BRIDGE_OUTPUT")
			case "watched preview":
				s.afterCapture = func(frame int) {
					if frame == 1 {
						change()
					}
					if frame == 2 {
						s.must("kill-session", "-t", "$1:")
					}
				}
				base := NewBaseDeps()
				base.Shell = s
				root := s.commandRoot(NewPreviewCommand(base))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				root.SetContext(ctx)
				root.SetArgs([]string{"preview", "$0", "--watch", "--interval", "1ms"})
				var output bytes.Buffer
				root.SetOut(&output)
				require.Error(t, root.Execute())
				require.Equal(t, 2, s.captures)
				require.Contains(t, output.String(), "ORIGINAL_OUTPUT")
				require.NotContains(t, output.String(), "REPLACEMENT_OUTPUT")
				require.NotContains(t, output.String(), "BRIDGE_OUTPUT")
			case "existing window", "default window":
				name := "$0"
				if action == "default window" {
					name = ""
				}
				target, err := c.ConnectWindow(model.WindowConnectOpts{Session: name, Name: "notes", Path: selected.Path, Command: "ticket36-input"})
				require.NoError(t, err)
				require.Equal(t, "$0:1", target)
				require.Equal(t, "1", s.must("display-message", "-p", "-t", "$1:", "#{window_index}"))
				require.Equal(t, []string{"$1"}, s.focused)
				s.ready("$1:1", "ticket36-input")
			case "create window", "default create":
				name := "$0"
				if action == "default create" {
					name = ""
				}
				target, err := c.ConnectWindow(model.WindowConnectOpts{Session: name, Background: true, Command: "exec sleep 120"})
				require.NoError(t, err)
				require.Equal(t, "renamed:2", target)
				require.Equal(t, selected.Path, s.must("display-message", "-p", "-t", "$1:2", "#{pane_current_path}"))
			case "implicit listing", "explicit listing":
				s.afterInventory = change
				base := NewBaseDeps()
				base.Shell = s
				root := s.commandRoot(NewWindowCommand(base))
				args := []string{"window", "list"}
				if action == "explicit listing" {
					args = append(args, "--target", "$0")
				}
				root.SetArgs(args)
				output := captureWindowStdout(t, root.Execute)
				require.Equal(t, "original\nnotes\n", output)
			}
			require.NotNil(t, replacementBefore, "the adversarial replacement was created")
			require.Equal(t, bridgeBefore, s.state("$0"), "bridge name, marker, selection and output are unchanged")
			require.Equal(t, replacementBefore, s.state("$2"), "replacement name, marker, selection and output are unchanged")
		})
	}
}

func TestExistingSessionGenerationGuardOnPrivateTmux(t *testing.T) {
	for _, operation := range []string{"attach", "switch", "rename", "capture", "list", "create", "send", "select"} {
		t.Run(operation, func(t *testing.T) {
			s := newExistingPrivateTmux(t)
			tm := tmux.NewTmux(NewBaseDeps().Os, s, "private-tmux")
			li := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			selected, found := li.FindTmuxSession("$0")
			require.True(t, found)
			_, err := tmux.VerifyLocalSession(tm, selected.TmuxSessionID, selected.TmuxServerGeneration)
			require.NoError(t, err)
			var bridgeBefore, replacementBefore []string
			s.beforeOperation = func(string) {
				s.beforeOperation = nil
				require.NoError(t, s.stop())
				s.start()
				s.newSession("$1", "$0", "replacement", "REPLACEMENT_OUTPUT")
				s.must("new-window", "-d", "-t", "$1:", "-n", "notes", "--", "exec sleep 120")
				require.NotEqual(t, selected.TmuxServerGeneration, s.must("display-message", "-p", "-t", "$1:", "#{pid}:#{start_time}:#{socket_path}"))
				bridgeBefore, replacementBefore = s.state("$0"), s.state("$1")
			}
			id, generation := selected.TmuxSessionID, selected.TmuxServerGeneration
			switch operation {
			case "attach":
				_, err = tm.AttachSession(id, generation)
			case "switch":
				_, err = tm.SwitchClientTarget("private-client", id, generation)
			case "rename":
				_, err = tm.RenameSession(id, "wrong", generation)
			case "capture":
				_, err = tm.CapturePane(id, generation)
			case "list":
				_, err = tm.ListWindows(id, generation)
			case "create":
				_, err = tm.NewWindowInSession(model.TmuxWindowOpts{TargetSession: id, Command: "exec sleep 120"}, generation)
			case "send":
				_, err = tm.SendKeys(id+":1", "wrong", generation)
			case "select":
				_, err = tm.SelectWindow(id+":1", generation)
			}
			require.Error(t, err)
			require.Empty(t, s.focused)
			require.Equal(t, bridgeBefore, s.state("$0"))
			require.Equal(t, replacementBefore, s.state("$1"), "old generation cannot authorize this server's reused ID")
		})
	}
}

func TestWatchedSessionRestartOnPrivateTmux(t *testing.T) {
	s := newExistingPrivateTmux(t)
	var bridgeBefore, replacementBefore []string
	s.afterCapture = func(frame int) {
		require.Equal(t, 1, frame, "watch must stop before capturing a reused ID")
		require.NoError(t, s.stop())
		s.start()
		s.newSession("$1", "$0", "replacement", "REPLACEMENT_OUTPUT")
		bridgeBefore, replacementBefore = s.state("$0"), s.state("$1")
	}
	base := NewBaseDeps()
	base.Shell = s
	root := s.commandRoot(NewPreviewCommand(base))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root.SetContext(ctx)
	root.SetArgs([]string{"preview", "$0", "--watch", "--interval", "1ms"})
	var output bytes.Buffer
	root.SetOut(&output)

	require.Error(t, root.Execute())

	require.Equal(t, 1, s.captures)
	require.Contains(t, output.String(), "ORIGINAL_OUTPUT")
	require.NotContains(t, output.String(), "REPLACEMENT_OUTPUT")
	require.NotContains(t, output.String(), "BRIDGE_OUTPUT")
	require.Equal(t, bridgeBefore, s.state("$0"))
	require.Equal(t, replacementBefore, s.state("$1"))
}

func TestDisappearedSessionGuardOnPrivateTmux(t *testing.T) {
	for _, operation := range []string{"rename", "preview"} {
		t.Run(operation, func(t *testing.T) {
			s := newExistingPrivateTmux(t)
			tm := tmux.NewTmux(NewBaseDeps().Os, s, "private-tmux")
			li := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			selected, found := li.FindTmuxSession("$0")
			require.True(t, found)
			_, err := tmux.VerifyLocalSession(tm, selected.TmuxSessionID, selected.TmuxServerGeneration)
			require.NoError(t, err)
			bridgeBefore := s.state("$0")
			var replacementBefore []string
			s.beforeOperation = func(string) {
				s.beforeOperation = nil
				s.must("kill-session", "-t", "$1:")
				// A literal ID-looking name cannot replace the vanished ID, even
				// if native target resolution falls back to that name.
				s.newSession("$2", "$1", "replacement", "REPLACEMENT_OUTPUT")
				replacementBefore = s.state("$2")
			}
			if operation == "rename" {
				_, err = tm.RenameSession(selected.TmuxSessionID, "wrong", selected.TmuxServerGeneration)
			} else {
				output, captureErr := tm.CapturePane(selected.TmuxSessionID, selected.TmuxServerGeneration)
				err = captureErr
				require.Empty(t, output)
			}
			require.Error(t, err)
			require.Equal(t, bridgeBefore, s.state("$0"))
			require.Equal(t, replacementBefore, s.state("$2"))
		})
	}
}
