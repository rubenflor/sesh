package picker

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/previewer"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPickerSelectionRetainsRow(t *testing.T) {
	for _, src := range []string{"tmux", "ssh", "zoxide", "dir", "config", "config_wildcard", "tmuxinator", "tmux-pane"} {
		for _, selection := range []string{"enter", "index", "alias enter", "alias auto"} {
			t.Run(src+"/"+selection, func(t *testing.T) {
				row := model.SeshSession{Src: src, Name: "work", Path: "/work"}
				if src == "tmux" {
					row.TmuxSessionID, row.TmuxServerGeneration = "$1", testServerGeneration
				} else if src == "ssh" {
					row.Name, row.Path = "ssh/host/work", ""
				}
				sessions := model.SeshSessions{OrderedIndex: []string{"selected"}, Directory: model.SeshSessionMap{"selected": row}}
				m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
					o.Aliases = map[string]Alias{"w": {Alias: "w", Target: row.Name, AutoConnect: true}}
				}))
				loaded, _ := m.Update(sessionsLoadedMsg{sessions: sessions})
				m = loaded.(Model)
				var key tea.Msg = tea.KeyPressMsg{Code: tea.KeyEnter}
				switch selection {
				case "index":
					m.filterInput.SetValue("#")
					m.applyFilter()
					key = tea.KeyPressMsg{Code: '1'}
				case "alias enter", "alias auto":
					m.filterInput.SetValue("w")
					m.applyFilter()
					if selection == "alias auto" {
						key = aliasAutoConnectMsg{seq: m.aliasSeq, alias: "w"}
					}
				}
				selected, _ := m.Update(key)
				require.Equal(t, row, selected.(Model).ChosenSession())
				require.Equal(t, row.Name, selected.(Model).Chosen(), "display names remain unchanged")
			})
		}
	}
}

func TestPickerPreviewCapturesRowBeforeDebounce(t *testing.T) {
	row := model.SeshSession{Src: "tmux", Name: "work", TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration}
	sessions := model.SeshSessions{OrderedIndex: []string{"selected"}, Directory: model.SeshSessionMap{"selected": row}}
	m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
		o.Preview = true
		o.PreviewFunc = func(selected model.SeshSession) (string, error) {
			require.Equal(t, row, selected)
			return "original output", nil
		}
	}))
	loaded, cmd := m.Update(sessionsLoadedMsg{sessions: sessions})
	m = loaded.(Model)
	fetch := previewFetch(cmd)
	require.NotNil(t, fetch)
	require.Equal(t, row, fetch.session)
	started, run := m.Update(*fetch)
	m = started.(Model)
	require.NotNil(t, run)
	// Replacing a visible row cannot change an already queued preview's target.
	m.filtered[0].item.session.TmuxSessionID = "$2"
	m.filtered[0].item.session.TmuxServerGeneration = "102:1700000001:/tmp/tmux-fixture"
	completed, _ := m.Update(run())
	m = completed.(Model)
	require.NoError(t, m.previewErr)
	require.Empty(t, m.previewContent, "an original-tuple result cannot describe the replacement row, even at the current sequence")
	_, staleFetch := m.Update(*fetch)
	require.Nil(t, staleFetch, "a queued fetch must also still belong to the highlighted identity")
}

func TestPickerLocalPreviewUsesOriginalIdentity(t *testing.T) {
	for _, state := range []string{"normal", "renamed", "missing ID", "missing generation", "unknown generation", "disappeared", "restarted", "marked", "lookup error"} {
		t.Run(state, func(t *testing.T) {
			row := model.SeshSession{Src: "tmux", Name: "work", TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration}
			live := &model.TmuxSession{ID: "$1", Name: "work", ServerGeneration: testServerGeneration}
			switch state {
			case "renamed":
				live.Name = "renamed"
			case "missing ID":
				row.TmuxSessionID = ""
			case "missing generation":
				row.TmuxServerGeneration = ""
			case "unknown generation":
				row.TmuxServerGeneration = "unknown"
			case "disappeared":
				live.ID = "$3"
			case "restarted":
				live.ServerGeneration = "102:1700000001:/tmp/tmux-fixture"
			case "marked":
				live.SSHBridgeIdentity = "ssh/host/work"
			}
			tm := tmux.NewMockTmux(t)
			if state != "missing ID" && state != "missing generation" && state != "unknown generation" {
				var lookupErr error
				if state == "lookup error" {
					lookupErr = assert.AnError
				}
				tm.EXPECT().ListSessions().Return([]*model.TmuxSession{live,
					{ID: "$2", Name: "work", ServerGeneration: testServerGeneration},
				}, lookupErr).Once()
			}
			success := state == "normal" || state == "renamed"
			if success {
				tm.EXPECT().CapturePane("$1", testServerGeneration).Return("original output", nil).Once()
			}
			// No name-preview expectations: a fallback must fail this test.
			p := &RealPicker{tmux: tm, previewer: previewer.NewMockPreviewer(t)}
			output, err := p.previewSession(row)
			if success {
				require.NoError(t, err)
				require.Equal(t, "original output", output)
			} else {
				require.Error(t, err)
				require.Empty(t, output)
			}
		})
	}
}

func TestPickerNonTmuxPreviewPreservesName(t *testing.T) {
	for _, src := range []string{"ssh", "zoxide", "dir", "config", "config_wildcard", "tmuxinator", "tmux-pane"} {
		t.Run(src, func(t *testing.T) {
			name := "work"
			if src == "ssh" {
				name = "ssh/host/work%2F1"
			}
			pr := previewer.NewMockPreviewer(t)
			pr.EXPECT().Preview(name).Return("existing preview", nil).Once()
			p := &RealPicker{tmux: tmux.NewMockTmux(t), previewer: pr}
			output, err := p.previewSession(model.SeshSession{Src: src, Name: name})
			require.NoError(t, err)
			require.Equal(t, "existing preview", output)
		})
	}
}

func TestPickerPreviewSameNameConfigToCachedTmux(t *testing.T) {
	for _, change := range []string{"rename", "restart"} {
		t.Run(change, func(t *testing.T) {
			// The local row is the cached snapshot from generation A, never
			// refreshed from the display name during this handoff.
			original := model.SeshSession{Src: "tmux", Name: "work", TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration}
			sessions := model.SeshSessions{
				OrderedIndex: []string{"config", "cached"},
				Directory: model.SeshSessionMap{
					"config": {Src: "config", Name: "work"},
					"cached": original,
				},
			}
			tm := tmux.NewMockTmux(t)
			li := lister.NewMockLister(t)
			pr := previewer.NewMockPreviewer(t)
			strategy := previewer.NewTmuxStrategy(li, tm)
			pr.EXPECT().Preview("work").RunAndReturn(strategy.Execute).Once()
			p := &RealPicker{tmux: tm, previewer: pr}
			m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
				o.Preview, o.PreviewFunc = true, p.previewSession
			}))
			m.width, m.height = 120, 24
			loaded, cmd := m.Update(sessionsLoadedMsg{sessions: sessions})
			m = loaded.(Model)
			fetch := previewFetch(cmd)
			require.NotNil(t, fetch)
			started, held := m.Update(*fetch)
			m = started.(Model)
			require.NotNil(t, held, "config preview has started but has not completed")
			oldSeq := m.previewSeq

			moved, localCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			m = moved.(Model)
			require.Equal(t, original, m.filtered[m.cursor].item.session)
			require.Equal(t, oldSeq+1, m.previewSeq, "same Name must not reuse a different source's request")
			require.NotNil(t, localCmd, "the original local tuple must get its own verified preview")

			live := []*model.TmuxSession{{ID: "$1", Name: "renamed", ServerGeneration: testServerGeneration}}
			replacement := model.SeshSession{Src: "tmux", Name: "work", TmuxSessionID: "$2", TmuxServerGeneration: testServerGeneration}
			if change == "restart" {
				replacement.TmuxSessionID, replacement.TmuxServerGeneration = "$1", "102:1700000001:/tmp/tmux-fixture"
				live = nil
			}
			live = append(live, &model.TmuxSession{ID: replacement.TmuxSessionID, Name: "work", ServerGeneration: replacement.TmuxServerGeneration})
			li.EXPECT().FindTmuxSession("work").Return(replacement, true).Once()
			tm.EXPECT().ListSessions().Return(live, nil).Twice()
			tm.EXPECT().CapturePane(replacement.TmuxSessionID, replacement.TmuxServerGeneration).Return("REPLACEMENT_OUTPUT", nil).Once()
			if change == "rename" {
				tm.EXPECT().CapturePane("$1", testServerGeneration).Return("ORIGINAL_OUTPUT", nil).Once()
			}

			// The old Name-based request really reaches the replacement. Its
			// completion must not be displayed under the cached local row.
			oldResult := held().(previewLoadedMsg)
			require.NoError(t, oldResult.err)
			require.Equal(t, "REPLACEMENT_OUTPUT", oldResult.content)
			completed, _ := m.Update(oldResult)
			m = completed.(Model)
			require.Equal(t, oldSeq+1, m.previewSeq)
			require.Empty(t, m.previewContent)
			require.NotContains(t, m.View().Content, "REPLACEMENT_OUTPUT")

			m = loadPreview(t, m, localCmd)
			if change == "restart" {
				require.Error(t, m.previewErr, "generation A cannot preview the generation-B replacement")
				require.Empty(t, m.previewContent)
			} else {
				require.NoError(t, m.previewErr)
				require.Equal(t, "ORIGINAL_OUTPUT", m.previewContent)
			}
			require.NotContains(t, m.View().Content, "REPLACEMENT_OUTPUT")
		})
	}
}

func TestPickerPreviewSameNameTargetChanges(t *testing.T) {
	original := model.SeshSession{Src: "tmux", Name: "work", TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration}
	transitions := []struct{ from, to model.SeshSession }{
		{original, model.SeshSession{Src: "tmux", Name: "work", TmuxSessionID: "$2", TmuxServerGeneration: testServerGeneration}},
		{original, model.SeshSession{Src: "tmux", Name: "work", TmuxSessionID: "$1", TmuxServerGeneration: "102:1700000001:/tmp/tmux-fixture"}},
		{model.SeshSession{Src: "config", Name: "work"}, model.SeshSession{Src: "zoxide", Name: "work"}},
		{model.SeshSession{Src: "config", Name: "work"}, model.SeshSession{Src: "config", Name: "elsewhere"}},
	}
	for _, src := range []string{"config", "zoxide", "dir", "ssh", "tmuxinator", "config_wildcard", "tmux-pane", "other"} {
		transitions = append(transitions, struct{ from, to model.SeshSession }{original, model.SeshSession{Src: src, Name: "work"}})
	}
	for _, transition := range transitions {
		from, target := transition.from, transition.to
		for _, state := range []string{"pending", "loaded"} {
			t.Run(from.Src+"-to-"+target.Src+"/"+target.Name+"/"+target.TmuxSessionID+"/"+target.TmuxServerGeneration+"/"+state, func(t *testing.T) {
				sessions := model.SeshSessions{OrderedIndex: []string{"original", "target"}, Directory: model.SeshSessionMap{"original": from, "target": target}}
				var asked []model.SeshSession
				m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
					o.Preview = true
					o.PreviewFunc = func(row model.SeshSession) (string, error) {
						asked = append(asked, row)
						if len(asked) == 1 {
							return "OLD_OUTPUT", nil
						}
						return "CURRENT_OUTPUT", nil
					}
				}))
				loaded, cmd := m.Update(sessionsLoadedMsg{sessions: sessions})
				m = loaded.(Model)
				fetch := previewFetch(cmd)
				require.NotNil(t, fetch)
				started, run := m.Update(*fetch)
				m = started.(Model)
				require.NotNil(t, run)
				oldResult := run()
				oldSeq := m.previewSeq
				if state == "loaded" {
					completed, _ := m.Update(oldResult)
					m = completed.(Model)
					require.Equal(t, "OLD_OUTPUT", m.previewContent)
				}

				moved, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				m = moved.(Model)
				require.Equal(t, oldSeq+1, m.previewSeq)
				m = loadPreview(t, m, cmd)
				require.Equal(t, []model.SeshSession{from, target}, asked)
				completed, _ := m.Update(oldResult)
				m = completed.(Model)
				require.Equal(t, "CURRENT_OUTPUT", m.previewContent, "old ID, generation or source cannot overwrite the current target")
			})
		}
	}
}

func TestPickerPreviewExactTargetIsReused(t *testing.T) {
	for _, src := range []string{"tmux", "config", "zoxide", "dir", "ssh", "tmuxinator", "config_wildcard", "tmux-pane", "other"} {
		t.Run(src, func(t *testing.T) {
			row := model.SeshSession{Src: src, Name: "work", Path: "/work"}
			if src == "tmux" {
				row.TmuxSessionID, row.TmuxServerGeneration = "$1", testServerGeneration
			}
			sessions := model.SeshSessions{OrderedIndex: []string{"row"}, Directory: model.SeshSessionMap{"row": row}}
			calls := 0
			m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
				o.Preview = true
				o.PreviewFunc = func(selected model.SeshSession) (string, error) {
					calls++
					require.Equal(t, row, selected)
					return "SAME_TARGET_OUTPUT", nil
				}
			}))
			loaded, cmd := m.Update(sessionsLoadedMsg{sessions: sessions})
			m = loaded.(Model)
			seq := m.previewSeq
			updated := row
			updated.Path, updated.Score = "/different-display-metadata", 42
			if src == "tmux" {
				updated.Name = "renamed"
			}
			sessions.Directory["row"] = updated
			reloaded, reused := m.Update(sessionsLoadedMsg{sessions: sessions})
			m = reloaded.(Model)
			require.Nil(t, reused, "the exact pending execution identity does not need another request")
			require.Equal(t, seq, m.previewSeq)
			m = loadPreview(t, m, cmd)
			reloaded, reused = m.Update(sessionsLoadedMsg{sessions: sessions})
			m = reloaded.(Model)
			require.Nil(t, reused, "the exact loaded identity remains reusable")
			require.Equal(t, seq, m.previewSeq)
			require.Equal(t, "SAME_TARGET_OUTPUT", m.previewContent)
			require.Equal(t, 1, calls)
		})
	}
}
