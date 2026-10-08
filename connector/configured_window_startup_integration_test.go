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

	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/startup"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

// Only mutation timing is injected; creation and input use production tmux.
type configuredWindowStartupTmux struct {
	tmux.Tmux
	afterCreate func(model.TmuxPane)
	beforeSend  func(string, string)
}

func (tm *configuredWindowStartupTmux) NewWindowPaneInSession(opts model.TmuxWindowOpts, generation ...string) (model.TmuxPane, error) {
	pane, err := tm.Tmux.NewWindowPaneInSession(opts, generation...)
	if err == nil {
		tm.afterCreate(pane)
	}
	return pane, err
}

func (tm *configuredWindowStartupTmux) SendKeys(target, command string, generation ...string) (string, error) {
	tm.beforeSend(target, command)
	return tm.Tmux.SendKeys(target, command, generation...)
}

func TestConfiguredWindowStartupOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	parent, err := os.MkdirTemp("/tmp", "sesh-startup-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Remove(parent)) })
	root, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	t.Setenv("TMPDIR", root)
	t.Setenv("TMUX", "")
	for _, change := range []string{"ordinary", "active pane changes", "active window changes", "pane disappears", "window disappears", "pane moves", "window moves", "rename and reindex", "server restarts", "multiple windows", "no script"} {
		t.Run(change, func(t *testing.T) {
			fixture, sh := newBridgePrivateFixture(t, binary)
			require.NoError(t, fixture.startAnchor())
			run := func(args ...string) string {
				t.Helper()
				out, err := sh.run(args...)
				require.NoError(t, err)
				return out
			}
			server := shell.Quote(binary) + " -S " + shell.Quote(fixture.socket) + " -f /dev/null"
			readyScript := filepath.Join(fixture.dir, "ready.sh")
			require.NoError(t, os.WriteFile(readyScript, []byte("#!/bin/sh\nexport PS1='' PS2=''\nprintf 'TICKET40_READY %s\\n' \"$TMUX_PANE\"\n"+server+" wait-for -S \"ready-$TMUX_PANE\"\nexec /bin/sh -i\n"), 0700))
			markerScript := filepath.Join(fixture.dir, "startup.sh")
			require.NoError(t, os.WriteFile(markerScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$TMUX_PANE\" > \"$1\"\nprintf 'TICKET40_SCRIPT %s\\n' \"$TMUX_PANE\"\n"+server+" wait-for -S \"$2\"\n"), 0700))
			ready := func(id string) {
				t.Helper()
				run("wait-for", "ready-"+id)
				require.Eventually(t, func() bool {
					return strings.Contains(run("capture-pane", "-p", "-t", id), "TICKET40_READY "+id)
				}, 3*time.Second, 10*time.Millisecond)
			}
			start := func() {
				t.Helper()
				run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/bridge")
				run("set-option", "-g", "default-shell", "/bin/sh")
				run("set-option", "-g", "default-command", shell.Quote(readyScript))
				require.Equal(t, "$1 @1 %1", run("new-session", "-d", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-s", "$0", "-n", "initial", "-c", fixture.dir, "--", "exec sleep 120"))
				require.Equal(t, "%2", run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "$1:@1"))
				ready("%2")
				for i := 3; i <= 5; i++ {
					require.Equal(t, fmt.Sprintf("%%%d", i), run("new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "$0:", "-n", "filler", "--", "exec sleep 120"))
				}
			}
			start()
			active := func() string {
				return run("display-message", "-p", "-t", "$1:", "#{session_id} #{window_id} #{pane_id}")
			}
			state := func(id string) []string {
				t.Helper()
				result := []string{run("list-windows", "-t", id+":", "-F", "#{window_id} #{window_index} #{window_active} #{pane_id}"), run("show-environment", "-t", id)}
				for _, pane := range strings.Fields(run("list-panes", "-s", "-t", id+":", "-F", "#{pane_id}")) {
					result = append(result, run("capture-pane", "-p", "-t", pane))
				}
				return result
			}
			var created []model.TmuxPane
			var events, targets, currents, bridgeBefore []string
			var currentBefore, otherBefore, replacementBefore string
			guarded := &configuredWindowStartupTmux{Tmux: tmux.NewTmux(nil, sh, binary)}
			guarded.afterCreate = func(pane model.TmuxPane) {
				t.Helper()
				created = append(created, pane)
				require.Equal(t, fmt.Sprintf("%%%d", len(created)+5), pane.PaneID)
				require.Equal(t, "$1 "+pane.WindowID+" "+pane.PaneID, active(), "creation initially selected its own pane")
				ready(pane.PaneID)
				events = append(events, "created "+pane.PaneID)
				switch change {
				case "active pane changes", "pane disappears", "pane moves":
					run("join-pane", "-d", "-s", "%2", "-t", pane.PaneID)
					run("select-pane", "-t", "%2")
					if change == "pane disappears" {
						run("kill-pane", "-t", pane.PaneID)
					} else if change == "pane moves" {
						run("join-pane", "-d", "-s", pane.PaneID, "-t", "%1")
					}
				case "window disappears":
					run("kill-window", "-t", pane.SessionID+":"+pane.WindowID)
				case "window moves":
					run("move-window", "-d", "-s", pane.SessionID+":"+pane.WindowID, "-t", "$0:8")
				case "rename and reindex":
					run("rename-window", "-t", pane.WindowID, "changed")
					run("move-window", "-d", "-s", pane.SessionID+":"+pane.WindowID, "-t", "$1:9")
				case "server restarts":
					require.NoError(t, fixture.stop())
					require.NoError(t, fixture.startAnchor())
					start()
					require.Equal(t, pane.SessionID+" "+pane.WindowID+" "+pane.PaneID, run("new-window", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-t", "$1:", "-n", "replacement"))
					ready(pane.PaneID)
					require.NotEqual(t, pane.ServerGeneration, run("display-message", "-p", "-t", pane.PaneID, bridgeServerFormat))
					replacementBefore = run("capture-pane", "-p", "-t", pane.PaneID)
				}
				if change != "ordinary" && change != "active pane changes" && change != "pane disappears" && change != "pane moves" {
					run("select-window", "-t", "$1:@1")
					run("select-pane", "-t", "%2")
				}
				currentBefore = active()
				currents = append(currents, currentBefore)
				if change != "ordinary" {
					require.True(t, strings.HasSuffix(currentBefore, " %2"), "the adversary genuinely became current")
					if change == "active pane changes" {
						require.Equal(t, "$1 "+pane.WindowID+" %2", currentBefore)
					} else if change == "active window changes" || change == "multiple windows" {
						require.Equal(t, "$1 @1 %2", currentBefore, "a different window is current")
					}
				}
				events = append(events, "current "+currentBefore)
				otherBefore = run("capture-pane", "-p", "-t", "%2")
				bridgeBefore = state("$0")
			}
			guarded.beforeSend = func(target, _ string) {
				require.Equal(t, currentBefore, active(), "the change must precede production input")
				targets = append(targets, target)
				events = append(events, "send "+target)
			}
			config := model.Config{WindowConfigs: []model.WindowConfig{{Name: "editor", Path: fixture.dir, StartupScript: "/bin/sh " + shell.Quote(markerScript) + " " + shell.Quote(filepath.Join(fixture.dir, "editor.marker")) + " done-editor"}}}
			if change == "multiple windows" {
				config.WindowConfigs = append(config.WindowConfigs, model.WindowConfig{Name: "server", Path: fixture.dir, StartupScript: "/bin/sh " + shell.Quote(markerScript) + " " + shell.Quote(filepath.Join(fixture.dir, "server.marker")) + " done-server"})
			} else if change == "no script" {
				config.WindowConfigs[0].StartupScript = ""
			}
			session := model.SeshSession{Name: "$0", Path: fixture.dir}
			h := home.NewMockHome(t)
			for _, window := range config.WindowConfigs {
				session.WindowNames = append(session.WindowNames, window.Name)
				h.EXPECT().ExpandPath(fixture.dir).Return(fixture.dir, nil).Once()
			}
			wantErr := change == "pane disappears" || change == "window disappears" || change == "pane moves" || change == "window moves" || change == "server restarts"
			li := lister.NewMockLister(t)
			if !wantErr {
				li.EXPECT().FindConfigSession(session.Name).Return(model.SeshSession{}, false).Once()
				li.EXPECT().FindConfigWildcard(session.Path).Return(model.WildcardConfig{}, false).Once()
			}
			_, err := startup.NewStartup(config, li, guarded, h, nil).Exec(session, "$1")
			if wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, created, len(config.WindowConfigs))
			for i, window := range config.WindowConfigs {
				marker := filepath.Join(fixture.dir, window.Name+".marker")
				if wantErr || window.StartupScript == "" {
					_, err := os.Stat(marker)
					require.ErrorIs(t, err, os.ErrNotExist)
					continue
				}
				run("wait-for", "done-"+window.Name)
				out, err := os.ReadFile(marker)
				require.NoError(t, err)
				require.Equal(t, created[i].PaneID+"\n", string(out), "the script actually executes in its own created pane")
				require.Contains(t, run("capture-pane", "-p", "-t", created[i].PaneID), "TICKET40_SCRIPT "+created[i].PaneID)
			}
			if change == "no script" {
				require.Empty(t, targets)
			} else {
				for i, pane := range created {
					target := pane.SessionID + ":" + pane.WindowID + "." + pane.PaneID
					require.Equal(t, target, targets[i])
					require.Equal(t, []string{"created " + pane.PaneID, "current " + currents[i], "send " + target}, events[i*3:i*3+3])
				}
			}
			require.Equal(t, otherBefore, run("capture-pane", "-p", "-t", "%2"), "the current substitute pane receives no input")
			require.Equal(t, bridgeBefore, state("$0"), "the marked bridge remains untouched")
			if change == "server restarts" {
				require.Equal(t, replacementBefore, run("capture-pane", "-p", "-t", "%6"), "reused IDs in another generation receive no input")
			}
			require.NoError(t, fixture.cleanup())
			require.True(t, fixture.removed)
		})
	}
}
