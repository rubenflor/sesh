package seshcli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/joshmedeski/sesh/v2/browser"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/worktree"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreeBrowseFocusedSessionOwnership(t *testing.T) {
	for _, inside := range []bool{true, false} {
		for _, marked := range []bool{true, false} {
			for _, flags := range [][]string{nil, {"--print"}, {"--pr"}, {"--pr", "--print"}} {
				t.Run(fmt.Sprintf("inside=%t/marked=%t/%v", inside, marked, flags), func(t *testing.T) {
					mOs := oswrap.NewMockOs(t)
					mShell := shell.NewMockShell(t)
					mWorktree := worktree.NewMockWorktree(t)
					mBrowser := browser.NewMockBrowser(t)
					mOs.EXPECT().Getenv("SESH_CLIENT").Return("")
					if inside {
						mOs.EXPECT().Getenv("TMUX").Return("/tmp/default,1,7")
						mOs.EXPECT().Getenv("TMUX_PANE").Return("%3")
						mShell.EXPECT().Cmd("tmux", "display-message", "-p", "#{session_id}").Return("$7", nil)
					} else {
						mOs.EXPECT().Getenv("TMUX").Return("")
						mOs.EXPECT().Getenv("TMUX_PANE").Return("")
						mShell.EXPECT().ListCmd("tmux", "list-clients", "-F", "#{client_name}::#{client_tty}::#{session_id}::#{client_activity}").
							Return([]string{"other::/dev/tty1::$1::100", "focused::/dev/tty2::$7::200"}, nil)
						mShell.EXPECT().Cmd("tmux", "display-message", "-p", "-t", "$7:", "#{session_id}").Return("$7", nil)
					}
					marker := ""
					if marked {
						marker = model.SSHBridgeIdentityEnv + "=ssh/host/work"
					}
					mShell.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$7", "--", model.SSHBridgeIdentityEnv).Return(marker, nil)
					deps := &Deps{Tmux: tmux.NewTmux(mOs, mShell, ""), Worktree: mWorktree, Browser: mBrowser,
						Config: model.Config{WorktreeConfigs: []model.WorktreeConfig{{Repo: "org/repo", Path: "/repo"}}}}
					cmd := newWorktreeBrowseCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
					cmd.SilenceErrors, cmd.SilenceUsage = true, true
					cmd.SetArgs(flags)
					var out bytes.Buffer
					cmd.SetOut(&out)
					cmd.SetErr(&out)
					printOnly, pr := false, false
					for _, flag := range flags {
						printOnly = printOnly || flag == "--print"
						pr = pr || flag == "--pr"
					}
					url := "https://github.com/org/repo/issues/409"
					if pr {
						url = "https://github.com/org/repo/pull/500"
					}
					if !marked {
						mShell.EXPECT().Cmd("tmux", "display-message", "-p", "-t", "$7:", "#{session_path}").Return("/repo/.wk/409", nil)
						mWorktree.EXPECT().BrowseURL("/repo/.wk/409", pr).Return(url, nil).Once()
						if !printOnly {
							mBrowser.EXPECT().Open(url).Return(nil).Once()
						}
					}
					if marked {
						require.ErrorContains(t, cmd.Execute(), "focus a local session")
						assert.Empty(t, mWorktree.Calls, "bridge must not reach BrowseURL or GitHub lookup")
						assert.Empty(t, mBrowser.Calls, "bridge must not open a browser")
						assert.Empty(t, out.String(), "bridge must not print a URL")
					} else {
						require.NoError(t, cmd.Execute())
						if printOnly {
							assert.Equal(t, url+"\n", out.String())
						} else {
							assert.Empty(t, out.String())
						}
					}
				})
			}
		}
	}
}

func TestWorktreeBrowseFailsClosedOnClientResolution(t *testing.T) {
	for _, failure := range []string{"listing", "selected client read", "disappeared"} {
		for _, flags := range [][]string{nil, {"--print"}, {"--pr"}, {"--pr", "--print"}} {
			t.Run(fmt.Sprintf("%s/%v", failure, flags), func(t *testing.T) {
				mOs := oswrap.NewMockOs(t)
				mShell := shell.NewMockShell(t)
				mWorktree := worktree.NewMockWorktree(t)
				mBrowser := browser.NewMockBrowser(t)
				format := "#{client_name}::#{client_tty}::#{session_id}::#{client_activity}"
				if failure == "listing" {
					mOs.EXPECT().Getenv("SESH_CLIENT").Return("")
					mOs.EXPECT().Getenv("TMUX").Return("")
					mOs.EXPECT().Getenv("TMUX_PANE").Return("")
					mShell.EXPECT().ListCmd("tmux", "list-clients", "-F", format).Return(nil, errors.New("list failed")).Once()
				} else {
					// An explicitly selected bridge client must never be replaced
					// by the ordinary local client still present on the server.
					mOs.EXPECT().Getenv("SESH_CLIENT").Return("bridge")
					if failure == "selected client read" {
						mShell.EXPECT().ListCmd("tmux", "list-clients", "-F", format).Return(nil, errors.New("read failed")).Once()
					} else {
						mShell.EXPECT().ListCmd("tmux", "list-clients", "-F", format).
							Return([]string{"local::/dev/tty2::$1::200"}, nil).Once()
					}
				}
				deps := &Deps{Tmux: tmux.NewTmux(mOs, mShell, ""), Worktree: mWorktree, Browser: mBrowser}
				cmd := newWorktreeBrowseCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetArgs(flags)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				require.ErrorContains(t, cmd.Execute(), "couldn't find the focused tmux session")
				assert.Empty(t, mWorktree.Calls)
				assert.Empty(t, mBrowser.Calls)
				assert.Empty(t, out.String())
				for _, call := range mShell.Calls {
					assert.Equal(t, "ListCmd", call.Method, "no fallback ID, marker, or path query is allowed")
				}
			})
		}
	}
}
