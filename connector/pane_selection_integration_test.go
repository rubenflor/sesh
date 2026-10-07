//go:build unix

package connector

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

// Inject only the mutation timing; inventory, ownership and selection remain
// native commands on the private server.
type paneSelectionTmux struct {
	tmux.Tmux
	afterInventory func()
}

func (tm *paneSelectionTmux) ListTmuxPanes() ([]*model.TmuxPane, error) {
	panes, err := tm.Tmux.ListTmuxPanes()
	if err == nil && tm.afterInventory != nil {
		after := tm.afterInventory
		tm.afterInventory = nil
		after()
	}
	return panes, err
}

func TestSelectedPaneIdentityOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	parent, err := os.MkdirTemp("/tmp", "sesh-selected-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Remove(parent)) })
	root, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	t.Setenv("TMPDIR", root)
	for _, change := range []string{"normal", "display takeover", "disappeared", "moved window", "moved session", "marked", "disappeared after validation", "moved after validation", "restart after validation"} {
		t.Run(change, func(t *testing.T) {
			fixture, sh := newBridgePrivateFixture(t, binary)
			require.NoError(t, fixture.startAnchor())
			sh.sessionID = "$1"
			run := func(args ...string) string {
				t.Helper()
				out, err := sh.run(args...)
				require.NoError(t, err)
				return out
			}
			start := func() {
				t.Helper()
				run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/bridge")
				require.Equal(t, "$1", run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "$0", "-n", "initial", "-c", fixture.dir, "--", "exec sleep 120"))
				run("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, "")
				require.Equal(t, "@2 %2", run("new-window", "-d", "-P", "-F", "#{window_id} #{pane_id}", "-t", "$1:", "-n", "filler", "--", "exec sleep 120"))
				for i := 3; i <= 6; i++ {
					require.Equal(t, fmt.Sprintf("%%%d", i), run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "$1:@2", "--", "exec sleep 120"))
				}
				run("select-layout", "-t", "$1:@2", "tiled")
				require.Equal(t, "@3 %7", run("new-window", "-d", "-P", "-F", "#{window_id} #{pane_id}", "-t", "$1:", "-n", "code", "-c", fixture.dir, "--", "exec sleep 120"))
				for i, title := range []string{"waiting", "idle"} {
					id := fmt.Sprintf("%%%d", 8+i)
					require.Equal(t, id, run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "$1:@3", "-c", fixture.dir, "--", "exec sleep 120"))
					run("select-pane", "-t", id, "-T", title)
				}
				run("select-pane", "-t", "%7", "-T", "editor")
				run("select-pane", "-t", "$1:@3.%9")
				run("select-window", "-t", "$1:@1")
			}
			start()
			osw := oswrap.NewMockOs(t)
			osw.EXPECT().Getenv("TMUX").Return(sh.socket + ",1,1").Maybe()
			tm := tmux.NewTmux(osw, sh, binary)
			li := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
			listed, err := li.ListTmuxPanes()
			require.NoError(t, err)
			selected := listed.Directory["tmux-pane:code/%7"]
			require.Equal(t, "code/editor", selected.Name)
			require.Equal(t, "$1", selected.TmuxSessionID)
			require.Equal(t, "@3", selected.TmuxWindowID)
			require.Equal(t, "%7", selected.TmuxPaneID)
			require.Equal(t, fixture.starts[0].Generation, selected.TmuxServerGeneration)
			require.Len(t, listed.OrderedIndex, 9, "marked bridge pane is excluded")
			active := func() string {
				return run("display-message", "-p", "-t", "$1:", "#{session_id} #{window_id} #{pane_id}")
			}
			state := func(id string) []string {
				return []string{
					run("list-windows", "-t", id+":", "-F", "#{window_id} #{window_active} #{pane_id}"),
					run("show-environment", "-t", id),
					run("capture-pane", "-p", "-t", id+":"),
				}
			}
			var bridgeBefore, userBefore []string
			takeover := func() {
				t.Helper()
				switch change {
				case "display takeover":
					run("select-pane", "-t", "%7", "-T", "renamed")
					run("rename-window", "-t", "$1:@3", "renamed")
					run("new-window", "-d", "-t", "$1:", "-n", "code", "--", "exec sleep 120")
					run("join-pane", "-d", "-s", "%8", "-t", "$1:@4")
				case "disappeared", "disappeared after validation":
					run("kill-pane", "-t", "%7")
				case "moved window", "moved after validation":
					run("join-pane", "-d", "-s", "%7", "-t", "$1:@2")
				case "moved session":
					run("join-pane", "-d", "-s", "%7", "-t", "$0:@0")
				case "marked":
					run("set-environment", "-t", "$1", model.SSHBridgeIdentityEnv, "ssh/fixture/new-bridge")
				case "restart after validation":
					require.NoError(t, fixture.stop())
					require.NoError(t, fixture.startAnchor())
					start()
					require.NotEqual(t, fixture.starts[0].Generation, fixture.starts[1].Generation)
					run("select-pane", "-t", "%7", "-T", "replacement")
				}
				if change != "normal" {
					run("select-pane", "-t", "%8", "-T", "editor")
					inventory := run("list-panes", "-a", "-F", "#{session_id} #{window_id} #{pane_id} #{window_name}/#{pane_title} #{pane_current_path}")
					require.Contains(t, inventory, "%8 code/editor "+selected.Path, "the replacement really has the old display name and path")
					if change != "marked" {
						fresh, err := li.ListTmuxPanes()
						require.NoError(t, err)
						key := "tmux-pane:code/%8"
						require.Equal(t, "code/editor", fresh.Directory[key].Name, "the old name lookup would match %8")
					}
				}
				bridgeBefore, userBefore = state("$0"), state("$1")
			}
			guarded := &paneSelectionTmux{Tmux: tm}
			if strings.HasSuffix(change, "after validation") {
				guarded.afterInventory = takeover
			} else {
				takeover()
			}
			c := NewConnector(model.Config{}, nil, nil, li, nil, nil, guarded, nil, nil, nil)
			_, err = c.ConnectSession(selected, model.ConnectOpts{})
			if change == "normal" || change == "display takeover" {
				require.NoError(t, err)
				require.Equal(t, "$1 @3 %7", active(), "connect must select the original tuple")
			} else {
				require.Error(t, err)
				require.Equal(t, userBefore, state("$1"), "rejected connect must not change selection")
			}
			require.Nil(t, guarded.afterInventory, "any scheduled mutation must actually run")
			require.Equal(t, bridgeBefore, state("$0"), "connect must leave the marked bridge untouched")
			require.Equal(t, "0", run("display-message", "-p", "-t", "%8", "#{pane_active}"), "replacement pane is never selected")
		})
	}
}
