//go:build unix

package tmux_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/convert"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oldSessionInventoryFormat = "#{session_activity}::#{session_alerts}::#{session_attached}::#{session_attached_list}::#{session_created}::#{session_format}::#{session_group}::#{session_group_attached}::#{session_group_attached_list}::#{session_group_list}::#{session_group_many_attached}::#{session_group_size}::#{session_grouped}::#{session_id}::#{session_last_attached}::#{session_many_attached}::#{session_marked}::#{session_name}::#{session_path}::#{session_stack}::#{session_windows}::#{pid}:#{start_time}:#{socket_path}"
const oldPaneInventoryFormat = "#{window_index}::#{window_name}::#{pane_index}::#{pane_title}::#{pane_current_command}::#{pane_current_path}::#{pane_id}::#{session_id}::#{window_id}::#{pid}:#{start_time}:#{socket_path}"

// Only the private socket and pane inventory scope are injected. All format
// expansion, ownership reads, parsing, filtering and operations remain native.
type inventoryPrivateShell struct {
	shell.Shell
	t                          *testing.T
	binary, dir, socket, scope string
	allPanes, withoutUTF8      bool
	probeDisplay               bool
}

func newInventoryPrivateShell(t *testing.T) *inventoryPrivateShell {
	return newInventoryPrivateShellSocket(t, "socket")
}

func newInventoryPrivateShellSocket(t *testing.T, socketName string) *inventoryPrivateShell {
	t.Helper()
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "sesh-inventory42-")
	require.NoError(t, err)
	dir, err = filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	s := &inventoryPrivateShell{t: t, binary: binary, dir: dir, socket: filepath.Join(dir, socketName), scope: "$0"}
	t.Cleanup(func() {
		pidText, _ := s.run("display-message", "-p", "#{pid}")
		pid, _ := strconv.Atoi(pidText)
		_, _ = s.run("kill-server")
		stopped := assert.Eventually(t, func() bool {
			out, err := s.run("display-message", "-p", "#{pid}")
			var exitErr *exec.ExitError
			if out != "" || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				return false
			}
			absent := strings.HasSuffix(err.Error(), "no server running on "+s.socket+"\n") ||
				strings.HasSuffix(err.Error(), "error connecting to "+s.socket+" (No such file or directory)\n")
			return absent && (pid == 0 || syscall.Kill(pid, 0) == syscall.ESRCH)
		}, 3*time.Second, 10*time.Millisecond, "private server must stop before removing %s", dir)
		if stopped {
			require.NoError(t, os.RemoveAll(dir))
		} else {
			t.Logf("retaining private fixture %s after unverified shutdown", dir)
		}
	})
	require.Equal(t, "$0 @0 %0", s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-s", "unrelated", "-n", "ordinary", "-c", dir, "--", "/bin/sh", "-c", "printf 'UNRELATED_READY\\n'; exec /bin/cat"))
	s.must("set-option", "-g", "default-shell", "/bin/sh")
	s.must("set-option", "-g", "default-command", "exec /bin/cat")
	s.ready("%0", "UNRELATED_READY")
	return s
}

func (s *inventoryPrivateShell) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.binary, append([]string{"-S", s.socket, "-f", "/dev/null"}, args...)...)
	cmd.WaitDelay = 250 * time.Millisecond
	// TMUX is absent; no ambient locale, PATH or shell startup files are used.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + s.dir, "SHELL=/bin/sh", "TERM=xterm", "LC_ALL=C", "LANG=C", "LC_CTYPE=C", "ENV=", "BASH_ENV="}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := strings.TrimSuffix(stdout.String(), "\n")
	if err != nil {
		return out, fmt.Errorf("private tmux %v: %w: %s", args, err, stderr.String())
	}
	return out, nil
}

func (s *inventoryPrivateShell) must(args ...string) string {
	s.t.Helper()
	out, err := s.run(args...)
	require.NoError(s.t, err, out)
	return out
}

func (s *inventoryPrivateShell) ready(pane, text string) {
	s.t.Helper()
	require.Eventually(s.t, func() bool {
		return s.must("display-message", "-p", "-t", pane, "#{pane_current_command}") == "cat" &&
			strings.Contains(s.must("capture-pane", "-p", "-t", pane), text)
	}, 3*time.Second, 10*time.Millisecond)
}

func (s *inventoryPrivateShell) Cmd(_ string, args ...string) (string, error) {
	return s.run(args...)
}

func (s *inventoryPrivateShell) CmdCapture(_ string, args ...string) (string, error) {
	return s.run(s.inventoryArgs(args)...)
}

func (s *inventoryPrivateShell) CmdCaptureRaw(ctx context.Context, _ string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := []string{"-i", "PATH=/usr/bin:/bin", "HOME=" + s.dir, "SHELL=/bin/sh", "TERM=xterm", "LC_ALL=C", "LANG=C", "LC_CTYPE=C", "ENV=", "BASH_ENV=", s.binary, "-S", s.socket, "-f", "/dev/null"}
	command = append(command, s.inventoryArgs(args)...)
	return shell.NewShell(execwrap.NewExec(), nil).CmdCaptureRaw(ctx, "/usr/bin/env", stdoutLimit, stderrLimit, command...)
}

func (s *inventoryPrivateShell) inventoryArgs(args []string) []string {
	args = append([]string(nil), args...)
	if s.withoutUTF8 && args[0] == "-u" {
		args = args[1:]
	}
	for i, arg := range args {
		if s.probeDisplay && i > 0 && args[i-1] == "-F" {
			// Probe basic native q: for every arbitrary display slot in both schemas.
			for _, field := range []string{"session_alerts", "session_attached_list", "session_group", "session_group_attached_list", "session_group_list", "session_name", "session_path", "session_stack", "window_name", "pane_title", "pane_current_command", "pane_current_path"} {
				args[i] = strings.ReplaceAll(args[i], "#{q:"+field+"}", "#{q:@probe}")
			}
		}
		if arg == "list-panes" {
			if s.allPanes {
				args[i+1] = "-a"
			} else if s.scope != "" {
				args = append(args, "-t", s.scope+":")
			}
		}
	}
	return args
}

func (s *inventoryPrivateShell) ListCmd(_ string, args ...string) ([]string, error) {
	out, err := s.run(args...)
	return strings.Split(out, "\n"), err
}

func TestTicket42SessionIdentityOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShell(t)
	// tmux escapes colons in session names, but its group name is arbitrary
	// display data. The old parser reads its fabricated $0 as session_id.
	group := "group::0::clients::sessions::0::1::0::$0::100::0::0::fabricated-name::/tmp::0::1::101:1700000000:/fake"
	require.Equal(t, "$1 @1 %1", s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-s", "$0", "-t", group))
	s.ready("%1", "")
	oldOutput := s.must("-u", "list-sessions", "-f", "#{==:#{session_id},$1}", "-F", oldSessionInventoryFormat)
	oldFields := strings.SplitN(oldOutput, "::", 22)
	require.Len(t, oldFields, 22)
	require.Equal(t, "$0", oldFields[13], "pre-fix parser substitutes unrelated $0 for real $1")
	require.Equal(t, "$0", s.must("display-message", "-p", "-t", oldFields[13]+":", "#{session_id}"))

	tm := tmux.NewTmux(nil, s, s.binary)
	sessions, err := tm.ListSessions()
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	byID := make(map[string]*model.TmuxSession)
	for _, session := range sessions {
		byID[session.ID] = session
	}
	require.Contains(t, byID, "$0")
	require.Contains(t, byID, "$1")
	require.Equal(t, "unrelated", byID["$0"].Name)
	require.Equal(t, "$0", byID["$1"].Name, "an ID-shaped name is still display data")
	require.Equal(t, group, byID["$1"].Group)
	require.Equal(t, s.must("display-message", "-p", "#{pid}:#{start_time}:#{socket_path}"), byID["$1"].ServerGeneration)
	before := s.must("capture-pane", "-p", "-t", "%0")
	_, err = tm.SendKeys(byID["$1"].ID+":", "TICKET42_INTENDED_INPUT", byID["$1"].ServerGeneration)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return strings.Count(s.must("capture-pane", "-p", "-t", "%1"), "TICKET42_INTENDED_INPUT") >= 2
	}, 3*time.Second, 10*time.Millisecond, "terminal echo and cat both receive input at $1")
	require.Equal(t, before, s.must("capture-pane", "-p", "-t", "%0"), "unrelated $0 stays untouched")
	t.Log("old native inventory parsed $1 as $0; production inventory retained $1, and guarded input reached only $1")
}

func TestTicket42BridgePaneIdentityOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShell(t)
	for i := 1; i <= 3; i++ {
		require.Equal(t, fmt.Sprintf("$%d @%d %%%d", i, i, i), s.must("new-session", "-d", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-s", fmt.Sprintf("session-%d", i), "-c", s.dir))
	}
	const identity = "ssh/fixture/work%2F42"
	s.must("set-environment", "-t", "$3", model.SSHBridgeIdentityEnv, identity)
	crafted := "bridge::0::fabricated-title::ssh::/fake::%0::$0::@0"
	s.must("-u", "rename-window", "-t", "$3:@3", crafted)
	s.must("-u", "select-pane", "-t", "%3", "-T", "bridge title")
	oldOutput := s.must("-u", "list-panes", "-s", "-t", "$3:", "-F", oldPaneInventoryFormat)
	oldFields := strings.SplitN(oldOutput, "::", 10)
	require.Len(t, oldFields, 10)
	require.Equal(t, []string{"%0", "$0", "@0"}, oldFields[6:9], "pre-fix parser forges the whole tuple")
	tm := tmux.NewTmux(nil, s, s.binary)
	oldMarker, err := tm.GetSessionEnvironment(oldFields[7], model.SSHBridgeIdentityEnv)
	require.NoError(t, err)
	require.Empty(t, oldMarker, "shifted session ID erases the bridge's marker")
	require.Equal(t, "$0 @0 %0", s.must("display-message", "-p", "-t", oldFields[7]+":"+oldFields[8]+"."+oldFields[6], "#{session_id} #{window_id} #{pane_id}"))

	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprintf("all panes=%t", all), func(t *testing.T) {
			s.scope, s.allPanes = "$3", all
			panes, err := tm.ListTmuxPanes()
			require.NoError(t, err)
			var bridge *model.TmuxPane
			for _, pane := range panes {
				if pane.SessionID == "$3" {
					bridge = pane
				} else {
					require.Empty(t, pane.SSHBridgeIdentity)
					require.NotEqual(t, crafted, pane.WindowName)
				}
			}
			require.NotNil(t, bridge)
			require.Equal(t, "$3", bridge.SessionID)
			require.Equal(t, "@3", bridge.WindowID)
			require.Equal(t, "%3", bridge.PaneID)
			require.Equal(t, crafted, bridge.WindowName)
			require.Equal(t, identity, bridge.SSHBridgeIdentity)
			require.Equal(t, s.must("display-message", "-p", "#{pid}:#{start_time}:#{socket_path}"), bridge.ServerGeneration)
			li := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			listed, err := li.ListTmuxPanes()
			require.NoError(t, err)
			require.NotContains(t, listed.Directory, "tmux-pane:"+crafted+"/%3")
			require.Len(t, listed.Directory, map[bool]int{false: 0, true: 3}[all])
		})
	}
	sessions, err := tm.ListSessions()
	require.NoError(t, err)
	for _, session := range sessions {
		if session.ID == "$3" {
			require.Equal(t, identity, session.SSHBridgeIdentity)
		} else {
			require.Empty(t, session.SSHBridgeIdentity)
		}
	}
	t.Log("old bridge inventory parsed $3/@3/%3 as $0/@0/%0 with an empty marker; production inventory retained the exact tuple and marker and discovery hid it")
}

func TestTicket42CLocaleFramingOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShell(t)
	text := ":::%\\café雪 $0 @0 %0 101:1700000000:/fake\n0::fake::0::title::cat::/fake::%0::$0::@0::101:1700000000:/fake"
	path := filepath.Join(s.dir, text)
	require.NoError(t, os.MkdirAll(path, 0700))
	group := "group-" + strings.ReplaceAll(text, "\n", " ")
	require.Equal(t, "$1 @1 %1", s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-s", "café雪", "-t", group, "-c", path))
	s.ready("%1", "")
	const marker = "ssh/fixture/caf%C3%A9"
	s.must("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, marker)
	// Native group/window names reject newlines; paths carry the embedded record.
	windowName := "window-" + strings.ReplaceAll(text, "\n", " ")
	s.must("-u", "rename-window", "-t", "$1:@1", windowName)
	title := "title-" + strings.ReplaceAll(text, "\n", " ")
	s.must("-u", "select-pane", "-t", "%1", "-T", title)
	// window_name retains tmux's own display escaping (including backslashes).
	nativeWindowName := s.must("-u", "display-message", "-p", "-t", "$1:@1", "#{window_name}")
	nativeTitle := s.must("-u", "display-message", "-p", "-t", "%1", "#{pane_title}")
	require.Contains(t, nativeTitle, "café雪")
	require.Equal(t, "$2 @2 %2", s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-s", `sesh-ssh-user::%\café雪 $0 @0 %0`, "-c", s.dir))
	nativeSessionName := s.must("-u", "display-message", "-p", "-t", "$2:", "#{session_name}")
	tm := tmux.NewTmux(nil, s, s.binary)
	generation := s.must("display-message", "-p", "#{pid}:#{start_time}:#{socket_path}")
	sessions, err := tm.ListSessions()
	require.NoError(t, err)
	require.Len(t, sessions, 3)
	for _, session := range sessions {
		require.Equal(t, generation, session.ServerGeneration)
		if session.ID == "$1" {
			require.Equal(t, "café雪", session.Name)
			require.Equal(t, path, session.Path)
			require.Equal(t, group, session.Group)
			require.Equal(t, []string{"café雪"}, session.GroupList)
			require.Equal(t, marker, session.SSHBridgeIdentity)
		} else {
			require.Empty(t, session.SSHBridgeIdentity)
			if session.ID == "$2" {
				require.Equal(t, nativeSessionName, session.Name, "preserve tmux's display escaping for delimiter-bearing names")
			}
		}
	}
	for _, all := range []bool{false, true} {
		s.scope, s.allPanes = "$1", all
		panes, err := tm.ListTmuxPanes()
		require.NoError(t, err)
		require.Len(t, panes, map[bool]int{false: 1, true: 3}[all])
		for _, pane := range panes {
			require.Equal(t, generation, pane.ServerGeneration)
			if pane.SessionID == "$1" {
				require.Equal(t, "@1", pane.WindowID)
				require.Equal(t, "%1", pane.PaneID)
				require.Equal(t, nativeWindowName, pane.WindowName)
				require.Equal(t, nativeTitle, pane.PaneTitle)
				require.Equal(t, path, pane.PanePath)
				require.Equal(t, marker, pane.SSHBridgeIdentity)
			} else {
				require.Empty(t, pane.SSHBridgeIdentity)
			}
		}
	}
	// Negative controls make these regressions sensitive to removal of -u.
	s.withoutUTF8 = true
	badSessions, err := tm.ListSessions()
	require.NoError(t, err)
	require.Len(t, badSessions, len(sessions))
	for _, session := range badSessions {
		if session.ID == "$1" {
			require.NotEqual(t, "café雪", session.Name, "removing -u loses display bytes under C locale")
		}
	}
	for _, all := range []bool{false, true} {
		s.allPanes = all
		badPanes, err := tm.ListTmuxPanes()
		require.NoError(t, err)
		require.Len(t, badPanes, map[bool]int{false: 1, true: 3}[all])
		for _, pane := range badPanes {
			if pane.SessionID == "$1" {
				require.Equal(t, "%1", pane.PaneID)
				require.NotEqual(t, nativeTitle, pane.PaneTitle)
			}
		}
	}
	s.withoutUTF8 = false
	t.Log("LC_ALL=C LANG=C LC_CTYPE=C with TMUX absent: UTF-8 and embedded records round-trip; removing -u loses display bytes without changing identities")
}

func TestTicket42NativeQuotedSpecialCharacters(t *testing.T) {
	s := newInventoryPrivateShell(t)
	s.probeDisplay = true
	tm := tmux.NewTmux(nil, s, s.binary)
	values := []string{"", " ", ":", "::", `"'`, `\`, "\n", "\t", "café雪", "$0 @0 %0", "#{pane_id}", "|&;<>()$`\\\"'*?[# =%", "before\nP1::0::\"fake\"::0::\"title\"::\"cat\"::\"/fake\"::%3::$3::@3::\"101:1700000000:/fake\"\nafter"}
	var everyByte strings.Builder
	for b := 1; b <= 255; b++ {
		everyByte.WriteByte(byte(b))
	}
	values = append(values, everyByte.String())
	rng := rand.New(rand.NewSource(42))
	for len(values) < 135 {
		value := make([]byte, rng.Intn(128))
		for i := range value {
			value[i] = byte(1 + rng.Intn(255))
		}
		values = append(values, string(value))
	}
	generation := s.must("-u", "display-message", "-p", "#{pid}:#{start_time}:#{socket_path}")
	for i, value := range values {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			argument := value
			if strings.HasSuffix(argument, ";") {
				argument = strings.TrimSuffix(argument, ";") + `\;`
			}
			s.must("set-option", "-g", "@probe", argument)
			native := s.must("-u", "display-message", "-p", "#{@probe}")
			require.Equal(t, value, native, "the option getter preserves the supplied bytes")
			sessions, err := tm.ListSessions()
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			session := sessions[0]
			require.Equal(t, "$0", session.ID)
			require.Equal(t, generation, session.ServerGeneration)
			require.Equal(t, native, session.Name)
			require.Equal(t, native, session.Path)
			require.Equal(t, native, session.Group)
			require.Equal(t, native, strings.Join(session.AttachedList, ","))
			require.Equal(t, native, strings.Join(session.GroupAttachedList, ","))
			require.Equal(t, native, strings.Join(session.GroupList, ","))
			var alerts []string
			for _, alert := range strings.Split(native, ",") {
				if alert != "" {
					alerts = append(alerts, alert)
				}
			}
			require.Equal(t, alerts, session.Alerts)
			require.Equal(t, convert.StringToIntSlice(native), session.Stack)
			panes, err := tm.ListTmuxPanes()
			require.NoError(t, err)
			require.Len(t, panes, 1)
			pane := panes[0]
			require.Equal(t, "$0", pane.SessionID)
			require.Equal(t, "@0", pane.WindowID)
			require.Equal(t, "%0", pane.PaneID)
			require.Equal(t, generation, pane.ServerGeneration)
			require.Equal(t, native, pane.WindowName)
			require.Equal(t, native, pane.PaneTitle)
			require.Equal(t, native, pane.PaneCommand)
			require.Equal(t, native, pane.PanePath)
		})
	}
	t.Logf("%d native cases across every session/pane arbitrary display slot, including all 255 non-NUL byte values, preserved bytes and native identities under C locale", len(values))
}

func TestTicket42QuotedGenerationOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShellSocket(t, "socket '"+`"\`+"\t雪::%0")
	generation := s.must("-u", "display-message", "-p", "#{pid}:#{start_time}:#{socket_path}")
	require.Contains(t, generation, s.socket)
	tm := tmux.NewTmux(nil, s, s.binary)
	sessions, err := tm.ListSessions()
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, generation, sessions[0].ServerGeneration)
	panes, err := tm.ListTmuxPanes()
	require.NoError(t, err)
	require.Len(t, panes, 1)
	require.Equal(t, generation, panes[0].ServerGeneration)
	_, err = tm.SendKeys("$0:", "QUOTED_SOCKET_INPUT", sessions[0].ServerGeneration)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return strings.Count(s.must("capture-pane", "-p", "-t", "%0"), "QUOTED_SOCKET_INPUT") >= 2
	}, 3*time.Second, 10*time.Millisecond)
}

func TestTicket42LiveCwdCommandInventoriesOnPrivateTmux(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed to continuously exec the pane process between commands")
	}
	s := newInventoryPrivateShell(t)
	pathA := filepath.Join(s.dir, "a :: \"\\ café雪 $0 @0 %0\nfirst")
	pathB := filepath.Join(s.dir, "b ::\tlonger complete cwd $3 @3 %3\nsecond")
	for _, path := range []string{pathA, pathB} {
		require.NoError(t, os.Mkdir(path, 0700))
	}
	const cycle = `import os,sys,time
a,b,body,deadline=sys.argv[1:5]
if time.monotonic()>float(deadline):sys.exit(0)
os.chdir(a);time.sleep(0.004)
os.execv('/bin/sh',['sh','-c','cd "$1"; /bin/sleep 0.004; exec "$5" -c "$2" "$3" "$1" "$2" "$4"','sh',b,body,a,deadline,sys.executable])
`
	const start = `import os,sys,time
a,b,body=sys.argv[1:4]
os.chdir(a);print('LIVE_READY',flush=True)
sys.stdin.readline()
os.execv(sys.executable,[sys.executable,'-c',body,a,b,body,str(time.monotonic()+120)])
`
	require.Equal(t, "$1 @1 %1", s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-s", "live", "-n", "live", "-c", s.dir, "--", python, "-c", start, pathA, pathB, cycle))
	require.Eventually(t, func() bool {
		return s.must("-u", "display-message", "-p", "-t", "%1", "#{pane_current_path}") == pathA &&
			strings.Contains(s.must("capture-pane", "-p", "-t", "%1"), "LIVE_READY")
	}, 5*time.Second, 10*time.Millisecond)
	rootCommand := s.must("-u", "display-message", "-p", "-t", "%1", "#{pane_current_command}")
	resolvedPython, err := filepath.EvalSymlinks(python)
	require.NoError(t, err)
	commands := map[string]bool{rootCommand: true, filepath.Base(python): true, filepath.Base(resolvedPython): true, "Python": true, "sh": true, "bash": true, "sleep": true}
	initialTitle := s.must("-u", "display-message", "-p", "-t", "%1", "#{pane_title}")
	window := `live::0::fake::ssh::/fake::%0::$0::@0 "\ café雪`
	title := "title::%3::$3::@3\t雪"
	s.must("-u", "rename-window", "-t", "$1:@1", window)
	s.must("-u", "select-pane", "-t", "%1", "-T", title)
	nativeWindow := s.must("-u", "display-message", "-p", "-t", "%1", "#{window_name}")
	nativeTitle := s.must("-u", "display-message", "-p", "-t", "%1", "#{pane_title}")
	titles := map[string]bool{initialTitle: true, nativeTitle: true}
	generation := s.must("-u", "display-message", "-p", "#{pid}:#{start_time}:#{socket_path}")
	s.scope = "$1"
	s.must("send-keys", "-t", "%1", "Enter")
	tm := tmux.NewTmux(nil, s, s.binary)
	var wg sync.WaitGroup
	var mu sync.Mutex
	pathsSeen, commandsSeen := make(map[string]int), make(map[string]int)
	errorsFound := make(chan error, 4)
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				panes, err := tm.ListTmuxPanes()
				if err != nil || len(panes) != 1 {
					errorsFound <- fmt.Errorf("pane batch count=%d: %v", len(panes), err)
					return
				}
				pane := panes[0]
				if pane.SessionID != "$1" || pane.WindowID != "@1" || pane.PaneID != "%1" || pane.WindowIndex != 0 || pane.PaneIndex != 0 || pane.ServerGeneration != generation || pane.WindowName != nativeWindow || !titles[pane.PaneTitle] || pane.SSHBridgeIdentity != "" || (pane.PanePath != pathA && pane.PanePath != pathB) || !commands[pane.PaneCommand] {
					errorsFound <- fmt.Errorf("incomplete or incorrectly partitioned pane: %+v", pane)
					return
				}
				mu.Lock()
				pathsSeen[pane.PanePath]++
				commandsSeen[pane.PaneCommand]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}
	require.Equal(t, 2000, pathsSeen[pathA]+pathsSeen[pathB])
	require.Positive(t, pathsSeen[pathA])
	require.Positive(t, pathsSeen[pathB])
	require.GreaterOrEqual(t, len(commandsSeen), 2, "native current_command really changed during collection")
	t.Logf("2000 concurrent production pane inventories: both complete cwd observations and commands %v; no extra records, mixed partitions or identity changes", commandsSeen)
}
