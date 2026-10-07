package seshcli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshmedeski/sesh/v2/github"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const existingGeneration = "101:1700000000:/tmp/tmux-fixture"

func TestEnrichKeepsCapturedSessionAndMetadata(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "attached", true: "named"}[explicit], func(t *testing.T) {
			li := lister.NewMockLister(t)
			m := tmux.NewMockTmux(t)
			n := namer.NewMockNamer(t)
			g := github.NewMockGithub(t)
			selected := model.SeshSession{Name: "$0", Path: "/old-metadata", TmuxSessionID: "$1", TmuxServerGeneration: existingGeneration}
			var args []string
			if explicit {
				args = []string{"$0"}
				li.EXPECT().FindTmuxSession("$0").Return(selected, true).Once()
			} else {
				li.EXPECT().GetAttachedTmuxSession().Return(selected, true).Once()
			}
			m.EXPECT().ListSessions().Return([]*model.TmuxSession{
				{ID: "$0", Name: "bridge", ServerGeneration: existingGeneration, SSHBridgeIdentity: "ssh/host/work"},
				{ID: "$1", Name: "renamed", Path: "/original", ServerGeneration: existingGeneration},
				{ID: "$2", Name: "$0", Path: "/replacement", ServerGeneration: existingGeneration},
			}, nil).Once()
			n.EXPECT().Name("/original").Return("base", nil).Once()
			g.EXPECT().Issue("/original").Return(github.Issue{Title: "issue"}, true, nil).Once()
			m.EXPECT().RenameSession("$1", "base — issue", existingGeneration).Return("", nil).Once()
			deps := &Deps{Lister: li, Tmux: m, Namer: n, BaseDeps: BaseDeps{Github: g}}

			require.NoError(t, runEnrich(deps, args))
		})
	}
}

func TestCLIExistingSessionTargetsFailClosed(t *testing.T) {
	for _, invalid := range []string{"missing ID", "missing generation", "disappeared", "restarted", "marked", "lookup error"} {
		for _, action := range []string{"enrich", "implicit listing", "explicit listing"} {
			t.Run(invalid+"/"+action, func(t *testing.T) {
				m := tmux.NewMockTmux(t)
				li := lister.NewMockLister(t)
				selected := model.SeshSession{Name: "$0", TmuxSessionID: "$1", TmuxServerGeneration: existingGeneration}
				live := &model.TmuxSession{ID: "$1", Name: "renamed", ServerGeneration: existingGeneration}
				switch invalid {
				case "missing ID":
					selected.TmuxSessionID = ""
				case "missing generation":
					selected.TmuxServerGeneration = ""
				case "disappeared":
					live.ID = "$3"
				case "restarted":
					live.ServerGeneration = "102:1700000001:/tmp/tmux-fixture"
				case "marked":
					live.SSHBridgeIdentity = "ssh/host/work"
				}
				if selected.TmuxSessionID != "" && selected.TmuxServerGeneration != "" {
					var lookupErr error
					if invalid == "lookup error" {
						lookupErr = assert.AnError
					}
					m.EXPECT().ListSessions().Return([]*model.TmuxSession{live, {ID: "$2", Name: "$0", ServerGeneration: existingGeneration}}, lookupErr).Once()
				}
				deps := &Deps{Tmux: m, Lister: li}
				var err error
				if action == "enrich" {
					li.EXPECT().GetAttachedTmuxSession().Return(selected, true).Once()
					err = runEnrich(deps, nil)
				} else {
					name := "$0"
					if action == "implicit listing" {
						name = ""
						m.EXPECT().IsAttached().Return(true).Once()
						li.EXPECT().GetAttachedTmuxSession().Return(selected, true).Once()
					} else {
						li.EXPECT().FindTmuxSession(name).Return(selected, true).Once()
					}
					_, _, err = windowSessionID(deps, name)
				}
				require.Error(t, err)
			})
		}
	}
}

func TestWindowListingKeepsCapturedSessionIdentity(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit", true: "explicit"}[explicit], func(t *testing.T) {
			m := tmux.NewMockTmux(t)
			li := lister.NewMockLister(t)
			selected := model.SeshSession{Name: "$0", TmuxSessionID: "$1", TmuxServerGeneration: existingGeneration}
			name := "$0"
			if explicit {
				li.EXPECT().FindTmuxSession(name).Return(selected, true).Once()
			} else {
				name = ""
				m.EXPECT().IsAttached().Return(true).Once()
				li.EXPECT().GetAttachedTmuxSession().Return(selected, true).Once()
			}
			m.EXPECT().ListSessions().Return([]*model.TmuxSession{
				{ID: "$1", Name: "renamed", ServerGeneration: existingGeneration},
				{ID: "$2", Name: "$0", ServerGeneration: existingGeneration},
			}, nil).Once()

			id, generation, err := windowSessionID(&Deps{Tmux: m, Lister: li}, name)

			require.NoError(t, err)
			require.Equal(t, "$1", id)
			require.Equal(t, existingGeneration, generation)
		})
	}
}

func TestWatchedTmuxPreviewRetainsFirstSelection(t *testing.T) {
	for _, stop := range []string{"capture error", "disappeared", "restarted", "marked"} {
		t.Run(stop, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "sesh.toml")
			require.NoError(t, os.WriteFile(config, []byte("cache = false\n"), 0600))
			s := shell.NewMockShell(t)
			reads, captures := 0, 0
			s.EXPECT().ListCmd("tmux", "list-sessions", "-F", mock.Anything).RunAndReturn(func(string, ...string) ([]string, error) {
				reads++
				name, replacementName, id, generation := "$0", "other", "$1", existingGeneration
				if reads > 1 {
					name = "renamed"
					replacementName = "$0"
				}
				if captures == 2 {
					if stop == "disappeared" {
						id = "$3"
					} else if stop == "restarted" {
						generation = "102:1700000001:/tmp/tmux-fixture"
					}
				}
				return []string{
					"200::::0::::100::0::::0::::::0::0::0::" + id + "::200::0::0::" + name + "::/original::::1::" + generation,
					"100::::0::::100::0::::0::::::0::0::0::$2::100::0::0::" + replacementName + "::/replacement::::1::" + generation,
				}, nil
			})
			s.EXPECT().CmdCapture("tmux", "show-environment", "-t", mock.Anything, "--", model.SSHBridgeIdentityEnv).RunAndReturn(func(string, ...string) (string, error) {
				if captures == 2 && stop == "marked" {
					return model.SSHBridgeIdentityEnv + "=ssh/host/work", nil
				}
				return "", nil
			})
			s.EXPECT().CmdCapture("tmux", "if-shell", "-F", "-t", "$1:", mock.Anything,
				"'capture-pane' '-e' '-p' '-t' '$1'", "if-shell").RunAndReturn(func(string, ...string) (string, error) {
				captures++
				if captures == 3 {
					return "", assert.AnError
				}
				return []string{"original frame", "original next frame"}[captures-1], nil
			})
			base := NewBaseDeps()
			base.Shell = s
			root := &cobra.Command{Use: "sesh", SilenceErrors: true, SilenceUsage: true}
			root.PersistentFlags().String("config", config, "")
			root.AddCommand(NewPreviewCommand(base))
			root.SetArgs([]string{"preview", "$0", "--watch", "--interval", "1ns"})
			var output bytes.Buffer
			root.SetOut(&output)

			err := root.Execute()

			require.Error(t, err)
			require.Equal(t, "original frame"+previewClearScreen+"original next frame", output.String())
			if stop == "capture error" {
				require.Equal(t, 3, captures)
			} else {
				require.Equal(t, 2, captures)
			}
		})
	}
}
