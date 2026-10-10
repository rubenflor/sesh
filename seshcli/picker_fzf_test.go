package seshcli

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/picker"
)

func TestPickerFZFSelectionPassedUnchangedToConnector(t *testing.T) {
	for _, row := range []model.SeshSession{
		{Src: "ssh", Name: "ssh/dev/work%20caf%C3%A9"},
		{Src: "dir", Name: "/tmp/⚡ literal $PATH", Path: "/tmp/⚡ literal $PATH"},
	} {
		t.Run(row.Src, func(t *testing.T) {
			sessions := model.SeshSessions{OrderedIndex: []string{"selected"}, Directory: model.SeshSessionMap{"selected": row}}
			li := lister.NewMockLister(t)
			li.EXPECT().List(mock.Anything).Return(sessions, nil).Once()
			pk := picker.NewMockPicker(t)
			pk.EXPECT().Pick(mock.Anything, mock.Anything).RunAndReturn(func(fetch picker.FetchFunc, opts picker.PickerOptions) (model.SeshSession, error) {
				m := picker.New(fetch, picker.Options{Context: opts.Context, Prompt: "> ", Keymap: "fzf", InputPosition: "bottom",
					FindDirectories: func(context.Context) (model.SeshSessions, error) { return sessions, nil },
				})
				var deliver func(tea.Cmd)
				deliver = func(cmd tea.Cmd) {
					if cmd == nil {
						return
					}
					msg := cmd()
					if batch, ok := msg.(tea.BatchMsg); ok {
						for _, child := range batch {
							deliver(child)
						}
						return
					}
					next, _ := m.Update(msg)
					m = next.(picker.Model)
				}
				deliver(m.Init())
				key := tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}
				if row.Src == "dir" {
					key.Code = 'f'
				}
				next, cmd := m.Update(key)
				m = next.(picker.Model)
				deliver(cmd)
				next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				return next.(picker.Model).ChosenSession(), nil
			}).Once()
			c := connector.NewMockConnector(t)
			c.EXPECT().ConnectSession(row, model.ConnectOpts{}).Return("", nil).Once()
			deps := &Deps{Lister: li, Picker: pk, Connector: c}
			cmd := newPickerCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			require.NoError(t, cmd.ExecuteContext(t.Context()))
		})
	}
}
