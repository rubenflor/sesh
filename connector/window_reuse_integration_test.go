//go:build unix

package connector

import (
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
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only mutation timing and interactive attachment are injected. Inventory,
// generation guards, input and window target resolution remain native tmux.
type windowReuseTmux struct {
	tmux.Tmux
	afterInventory func([]*model.TmuxWindow)
	beforeSelect   func()
	beforeFocus    func()
}

func (tm *windowReuseTmux) ListWindows(target string, generation ...string) ([]*model.TmuxWindow, error) {
	windows, err := tm.Tmux.ListWindows(target, generation...)
	if err == nil && tm.afterInventory != nil {
		after := tm.afterInventory
		tm.afterInventory = nil
		after(windows)
	}
	return windows, err
}

func (tm *windowReuseTmux) SelectWindow(target string, generation ...string) (string, error) {
	if tm.beforeSelect != nil {
		before := tm.beforeSelect
		tm.beforeSelect = nil
		before()
	}
	return tm.Tmux.SelectWindow(target, generation...)
}

func (tm *windowReuseTmux) SwitchOrAttach(target string, opts model.ConnectOpts, generation ...string) (string, error) {
	if tm.beforeFocus != nil {
		before := tm.beforeFocus
		tm.beforeFocus = nil
		before()
	}
	return tm.Tmux.SwitchOrAttach(target, opts, generation...)
}

type windowReuseShell struct {
	*panePrivateShell
	focused string
}

func (s *windowReuseShell) CmdWithOutput(_ string, args ...string) (string, error) {
	// Replace the interactive attach with selection of its exact target inside
	// the unchanged native guard; the test never attaches a real terminal.
	args = append([]string(nil), args...)
	if args[0] == "if-shell" {
		args[len(args)-2] = strings.Replace(args[len(args)-2], "'attach-session'", "'select-window'", 1)
	} else if args[0] == "attach-session" {
		args[0] = "select-window"
	}
	if _, err := s.run(args...); err != nil {
		return "", err
	}
	var err error
	s.focused, err = s.run("display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}")
	return "", err
}

func TestExistingWindowReuseOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	parent, err := os.MkdirTemp("/tmp", "sesh-window-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Remove(parent)) })
	root, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	t.Setenv("TMPDIR", root)
	const input = "TICKET39_INPUT"
	for _, test := range []struct {
		name, change, phase, command string
		background, wantErr          bool
	}{
		{"unchanged", "normal", "inventory", input, false, false},
		{"index takeover", "reindex", "inventory", input, false, false},
		{"renamed index takeover", "rename", "inventory", input, false, false},
		{"disappeared", "disappeared", "inventory", input, false, true},
		{"moved to bridge", "moved", "inventory", input, false, true},
		{"session disappeared", "session disappeared", "inventory", input, false, true},
		{"server restarted", "restart", "inventory", input, false, true},
		{"selection after reindex", "reindex", "inventory", "", false, false},
		{"selection disappeared", "disappeared", "inventory", "", false, true},
		{"background input after reindex", "reindex", "inventory", input, true, false},
		{"background no input after reindex", "reindex", "inventory", "", true, false},
		{"background no input disappeared", "disappeared", "inventory", "", true, true},
		{"disappeared before selection", "disappeared", "select", input, false, true},
		{"reindex before focus", "reindex", "focus", input, false, false},
		{"disappeared before focus", "disappeared", "focus", input, false, true},
		{"moved before focus", "moved", "focus", input, false, true},
		{"restart before focus", "restart", "focus", input, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, private := newBridgePrivateFixture(t, binary)
			require.NoError(t, fixture.startAnchor())
			sh := &windowReuseShell{panePrivateShell: private}
			run := func(args ...string) string {
				t.Helper()
				out, err := sh.run(args...)
				require.NoError(t, err)
				return out
			}
			ready := func(id, marker string) {
				t.Helper()
				require.Eventually(t, func() bool {
					return strings.Contains(run("capture-pane", "-p", "-t", id), marker) && run("display-message", "-p", "-t", id, "#{pane_current_command}") == "cat"
				}, 3*time.Second, 10*time.Millisecond)
			}
			create := func(id, index, marker string) {
				t.Helper()
				require.Equal(t, id, run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$1:"+index, "-n", "notes", "-c", fixture.dir, "--", "printf '"+marker+"\\n'; exec cat"))
				ready(id, marker)
			}
			start := func() {
				t.Helper()
				run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/bridge")
				require.Equal(t, "$1 @1", run("new-session", "-d", "-P", "-F", "#{session_id} #{window_id}", "-s", "$0", "-n", "initial", "-c", fixture.dir, "--", "exec sleep 120"))
				run("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, "")
				for i := 2; i <= 6; i++ {
					require.Equal(t, fmt.Sprintf("@%d", i), run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$0:", "-n", "filler", "--", "exec sleep 120"))
				}
				create("@7", "4", "WINDOW_A")
			}
			start()
			osw := oswrap.NewMockOs(t)
			osw.EXPECT().Getenv("TMUX").Return("").Maybe()
			realTmux := tmux.NewTmux(osw, sh, binary)
			li := lister.NewLister(model.Config{}, nil, realTmux, nil, nil, nil, nil, nil)
			zo := zoxide.NewMockZoxide(t)
			zo.EXPECT().Add(fixture.dir).Return(nil).Once()
			guarded := &windowReuseTmux{Tmux: realTmux}
			state := func(id string) []string {
				t.Helper()
				result := []string{
					run("list-windows", "-t", id+":", "-F", "#{window_id} #{window_index} #{window_name} #{window_active} #{pane_id}"),
					run("show-environment", "-t", id),
				}
				for _, paneID := range strings.Fields(run("list-panes", "-s", "-t", id+":", "-F", "#{pane_id}")) {
					result = append(result, run("capture-pane", "-p", "-t", paneID))
				}
				return result
			}
			waitInput := func() {
				t.Helper()
				require.Eventually(t, func() bool {
					return strings.Count(run("capture-pane", "-p", "-t", "@7"), input) >= 2
				}, 3*time.Second, 10*time.Millisecond, "both terminal echo and cat must have received input at @7")
			}
			var bridgeBefore, userBefore, replacementBefore []string
			var activeBefore string
			mutate := func() {
				t.Helper()
				if test.phase != "inventory" {
					waitInput()
				}
				index := "4"
				switch test.change {
				case "normal":
					index = "5"
				case "reindex", "rename":
					run("move-window", "-d", "-s", "$1:@7", "-t", "$1:3")
					if test.change == "rename" {
						run("rename-window", "-t", "$1:@7", "renamed")
						run("rename-session", "-t", "$1", "renamed session")
					}
				case "disappeared":
					run("kill-window", "-t", "$1:@7")
				case "moved":
					run("move-window", "-d", "-s", "$1:@7", "-t", "$0:8")
				case "session disappeared":
					run("kill-session", "-t", "$1:")
					require.Equal(t, "$2 @8", run("new-session", "-d", "-P", "-F", "#{session_id} #{window_id}", "-s", "$0", "-n", "notes", "--", "printf 'WINDOW_B\\n'; exec cat"))
					run("move-window", "-d", "-s", "$2:@8", "-t", "$2:4")
					ready("@8", "WINDOW_B")
				case "restart":
					require.NoError(t, fixture.stop())
					require.NoError(t, fixture.startAnchor())
					start()
					require.NotEqual(t, fixture.starts[0].Generation, fixture.starts[1].Generation)
					index = "5" // Both $1 and @7 deliberately collide in the new generation.
				}
				if test.change != "session disappeared" {
					create("@8", index, "WINDOW_B")
					run("select-window", "-t", "$1:@1")
					activeBefore = run("display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}")
					userBefore = state("$1")
					if index == "4" {
						require.Equal(t, "$1 @8", run("display-message", "-p", "-t", "$1:4", "#{session_id} #{window_id}"), "the old index target really resolves to the replacement")
					}
				}
				bridgeBefore = state("$0")
				replacementBefore = []string{run("capture-pane", "-p", "-t", "@8"), run("display-message", "-p", "-t", "@8", "#{window_id} #{pane_id} #{pane_pid} #{window_active}")}
			}
			guarded.afterInventory = func(windows []*model.TmuxWindow) {
				var selected *model.TmuxWindow
				for _, window := range windows {
					if window.Name == "notes" {
						selected = window
					}
				}
				require.NotNil(t, selected)
				require.Equal(t, "@7", selected.ID)
				require.Equal(t, fixture.starts[0].Generation, selected.ServerGeneration)
				require.Equal(t, 4, selected.Index)
				require.Equal(t, "$1 @7", run("display-message", "-p", "-t", "$1:4", "#{session_id} #{window_id}"))
				switch test.phase {
				case "inventory":
					mutate()
				case "select":
					guarded.beforeSelect = mutate
				case "focus":
					guarded.beforeFocus = mutate
				}
			}
			c := NewConnector(model.Config{}, nil, nil, li, nil, nil, guarded, zo, nil, nil)

			target, err := c.ConnectWindow(model.WindowConnectOpts{Session: "$0", Name: "notes", Path: fixture.dir, Command: test.command, Background: test.background})

			require.Nil(t, guarded.afterInventory)
			require.Nil(t, guarded.beforeSelect)
			require.Nil(t, guarded.beforeFocus, "the scheduled mutation must actually run")
			require.NotNil(t, replacementBefore)
			if test.wantErr {
				require.Error(t, err)
				require.Empty(t, target)
				require.Empty(t, sh.focused, "rejection must not fall back to another window")
				if userBefore != nil {
					assert.Equal(t, userBefore, state("$1"), "no side effects after failed identity validation")
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, "$0:4", target, "public name:index output stays unchanged")
				if test.command != "" {
					waitInput()
				}
				if test.background {
					require.Empty(t, sh.focused)
					require.Equal(t, activeBefore, run("display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}"))
				} else {
					require.Equal(t, "$1 @7", sh.focused, "focus must reach the originally selected @7")
					require.Equal(t, "$1 @7", run("display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}"))
				}
			}
			assert.Equal(t, replacementBefore, []string{run("capture-pane", "-p", "-t", "@8"), run("display-message", "-p", "-t", "@8", "#{window_id} #{pane_id} #{pane_pid} #{window_active}")}, "replacement input/output and focus remain untouched")
			assert.Equal(t, bridgeBefore, state("$0"), "every marked bridge window remains untouched")
		})
	}
}

func TestExistingWindowInventoryTextOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	parent, err := os.MkdirTemp("/tmp", "sesh-window-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Remove(parent)) })
	root, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	t.Setenv("TMPDIR", root)
	for _, test := range []struct{ name, windowName, pathSuffix, locale string }{
		{"delimiter path", "notes", "A::0::@8", "C.UTF-8"},
		{"delimiter window name", "notes::0::@8", "plain", "C.UTF-8"},
		{"percent backslash UTF-8", "notes%3A::\\n雪", "A%::\\x3A::@8雪", "C.UTF-8"},
		{"newline window name", "notes\n@8::0::@7", "plain", "C.UTF-8"},
		{"newline path", "notes", "A\n@8::4::0", "C.UTF-8"},
		{"C locale UTF-8 path", "notes", "normal-雪", "C"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, private := newBridgePrivateFixture(t, binary)
			// panePrivateShell.run removes TMUX from every child environment.
			t.Setenv("LC_ALL", test.locale)
			t.Setenv("LC_CTYPE", "C")
			t.Setenv("LANG", "C")
			require.NoError(t, fixture.startAnchor())
			sh := &windowReuseShell{panePrivateShell: private}
			run := func(args ...string) string {
				t.Helper()
				out, err := sh.run(args...)
				require.NoError(t, err)
				return out
			}
			run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/bridge")
			require.Equal(t, "$1 @1", run("new-session", "-d", "-P", "-F", "#{session_id} #{window_id}", "-s", "$0", "-n", "initial", "-c", fixture.dir, "--", "exec sleep 120"))
			run("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, "")
			for i := 2; i <= 6; i++ {
				require.Equal(t, fmt.Sprintf("@%d", i), run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$0:", "--", "exec sleep 120"))
			}
			windowPath := filepath.Join(fixture.dir, test.pathSuffix)
			require.NoError(t, os.MkdirAll(windowPath, 0700))
			require.Equal(t, "@7", run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$1:4", "-n", test.windowName, "-c", windowPath, "--", "printf 'WINDOW_A\\n'; exec cat"))
			require.Equal(t, "@8", run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$1:5", "-n", "other", "-c", fixture.dir, "--", "printf 'WINDOW_B\\n'; exec cat"))
			for id, marker := range map[string]string{"@7": "WINDOW_A", "@8": "WINDOW_B"} {
				require.Eventually(t, func() bool {
					return strings.Contains(run("capture-pane", "-p", "-t", id), marker) && run("display-message", "-p", "-t", id, "#{pane_current_command}") == "cat"
				}, 3*time.Second, 10*time.Millisecond)
			}
			run("select-window", "-t", "$1:@1")
			require.Equal(t, "$1 @8", run("display-message", "-p", "-t", "$1:@8", "#{session_id} #{window_id}"), "the other ID really belongs to the verified session")
			osw := oswrap.NewMockOs(t)
			osw.EXPECT().Getenv("TMUX").Return("").Maybe()
			realTmux := tmux.NewTmux(osw, sh, binary)
			generation := fixture.starts[0].Generation
			otherOutput, err := realTmux.CapturePane("$1:@8", generation)
			require.NoError(t, err, "the existing guard would accept a substituted @8")
			require.Contains(t, otherOutput, "WINDOW_B")
			windows, err := realTmux.ListWindows("$1", generation)
			require.NoError(t, err)
			require.Len(t, windows, 3)
			assert.Equal(t, &model.TmuxWindow{ID: "@7", ServerGeneration: generation, Name: test.windowName, Path: windowPath, Index: 4}, windows[1], "display text must never shift the native ID or generation")
			allWindows, err := realTmux.ListAllWindows()
			require.NoError(t, err)
			assert.Equal(t, *windows[1], allWindows["$0"][1], "both inventory callers use the same framing")
			protected := func() []string {
				return []string{
					run("capture-pane", "-p", "-t", "@8"),
					run("display-message", "-p", "-t", "@8", "#{window_id} #{pane_id} #{pane_pid} #{window_active}"),
					run("list-windows", "-t", "$0:", "-F", "#{window_id} #{window_index} #{window_active} #{pane_id}"),
					run("show-environment", "-t", "$0"),
				}
			}
			before := protected()
			li := lister.NewLister(model.Config{}, nil, realTmux, nil, nil, nil, nil, nil)
			zo := zoxide.NewMockZoxide(t)
			zo.EXPECT().Add(fixture.dir).Return(nil).Once()
			c := NewConnector(model.Config{}, nil, nil, li, nil, nil, realTmux, zo, nil, nil)
			const input = "TICKET39_FRAMED_INPUT"

			result, err := c.ConnectWindow(model.WindowConnectOpts{Session: "$0", Name: test.windowName, Path: fixture.dir, Command: input})

			require.NoError(t, err)
			assert.Equal(t, "$0:4", result)
			assert.Equal(t, "$1 @7", sh.focused)
			assert.Equal(t, "$1 @7", run("display-message", "-p", "-t", "$1:", "#{session_id} #{window_id}"))
			assert.Eventually(t, func() bool { return strings.Count(run("capture-pane", "-p", "-t", "@7"), input) >= 2 }, 3*time.Second, 10*time.Millisecond, "both terminal echo and cat must receive input at @7")
			assert.NotContains(t, run("capture-pane", "-p", "-t", "@8"), input)
			assert.Equal(t, before, protected(), "@8 and the marked bridge remain untouched")
		})
	}
}

// Removing only the new client option reproduces the pre-fix inventory call.
type windowInventoryWithoutUTF8Shell struct {
	*panePrivateShell
}

func (s *windowInventoryWithoutUTF8Shell) ListCmd(binary string, args ...string) ([]string, error) {
	if len(args) != 0 && args[0] == "-u" {
		args = args[1:]
	}
	return s.panePrivateShell.ListCmd(binary, args...)
}

func TestExistingWindowInventoryCLocaleSessionOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	parent, err := os.MkdirTemp("/tmp", "sesh-window-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Remove(parent)) })
	root, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	t.Setenv("TMPDIR", root)
	fixture, private := newBridgePrivateFixture(t, binary)
	// The native runner also removes TMUX from every child environment.
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_CTYPE", "C")
	t.Setenv("LANG", "C")
	require.NoError(t, fixture.startAnchor())
	run := func(args ...string) string {
		t.Helper()
		out, err := private.run(args...)
		require.NoError(t, err)
		return out
	}
	run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/bridge")
	for i := 1; i <= 6; i++ {
		require.Equal(t, fmt.Sprintf("@%d", i), run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$0:", "-n", "filler", "--", "exec sleep 120"))
	}
	generation := fixture.starts[0].Generation
	fakeName := func(index int, name string) string {
		return fmt.Sprintf("@8::%d::0::5::%d::%d::%s", index, len(fixture.dir), len(generation), name)
	}
	nameA, nameB := fakeName(4, "notes"), fakeName(5, "other")
	header := fmt.Sprintf("@7::4::1::%d::%d::%d::", len(nameA), len(fixture.dir), len(generation))
	// Each 雪 is three bytes but is sanitized into two underscores without -u.
	// The missing bytes exactly consume @7's header in the pre-fix parser.
	sessionName := strings.Repeat("雪", len(header))
	require.Equal(t, "$1 @7", run("new-session", "-d", "-P", "-F", "#{session_id} #{window_id}", "-s", sessionName, "-n", nameA, "-c", fixture.dir, "--", "exec sleep 120"))
	run("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, "")
	run("move-window", "-s", "$1:@7", "-t", "$1:4")
	require.Equal(t, "@8", run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "$1:5", "-n", nameB, "-c", fixture.dir, "--", "exec sleep 120"))
	require.Equal(t, "$1 @7 4 1", run("display-message", "-p", "-t", "$1:@7", "#{session_id} #{window_id} #{window_index} #{window_active}"))
	require.Equal(t, "$1 @8 5 0", run("display-message", "-p", "-t", "$1:@8", "#{session_id} #{window_id} #{window_index} #{window_active}"))
	require.Equal(t, generation, run("display-message", "-p", "-t", "$1:", bridgeServerFormat))

	legacy := tmux.NewTmux(nil, &windowInventoryWithoutUTF8Shell{panePrivateShell: private}, binary)
	corrupted, err := legacy.ListAllWindows()
	require.NoError(t, err, "the old call accepts the corrupted record without an error")
	corruptedSession := strings.Repeat("_", 2*len(header)) + header
	require.Equal(t, []model.TmuxWindow{{ID: "@8", Index: 4, Name: "notes", Path: fixture.dir, ServerGeneration: generation}}, corrupted[corruptedSession], "real @7's display payload becomes a trusted @8 with the real generation")
	t.Logf("without -u: session declares %d bytes but emits %d; real @7 parses as @8", len(sessionName), 2*len(header))

	realTmux := tmux.NewTmux(nil, private, binary)
	_, err = realTmux.CapturePane("$1:@8", generation)
	require.NoError(t, err, "@8 really exists in the same session and generation")
	windows, err := realTmux.ListAllWindows()
	require.NoError(t, err)
	require.Len(t, windows, 2, "embedded headers must not create session records")
	require.Len(t, windows["anchor"], 7)
	assert.Equal(t, []model.TmuxWindow{
		{ID: "@7", Index: 4, Active: true, Name: nameA, Path: fixture.dir, ServerGeneration: generation},
		{ID: "@8", Index: 5, Name: nameB, Path: fixture.dir, ServerGeneration: generation},
	}, windows[sessionName], "UTF-8 session names and fake headers round-trip as payload, preserving both real window IDs")
}
