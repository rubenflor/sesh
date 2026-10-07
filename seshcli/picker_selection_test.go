package seshcli

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/picker"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPickerConnectRetainsSelectedIdentity(t *testing.T) {
	for _, state := range []string{"normal", "renamed", "missing ID", "missing generation", "unknown generation", "disappeared", "restarted", "marked", "lookup error"} {
		t.Run(state, func(t *testing.T) {
			row := model.SeshSession{Src: "tmux", Name: "work", Path: "/original", TmuxSessionID: "$1", TmuxServerGeneration: existingGeneration}
			live := &model.TmuxSession{ID: "$1", Name: "work", ServerGeneration: existingGeneration}
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
			pk := picker.NewMockPicker(t)
			pk.EXPECT().Pick(mock.Anything, mock.Anything).Return(row, nil).Once()
			tm := tmux.NewMockTmux(t)
			if state != "missing ID" && state != "missing generation" && state != "unknown generation" {
				var lookupErr error
				if state == "lookup error" {
					lookupErr = assert.AnError
				}
				tm.EXPECT().ListSessions().Return([]*model.TmuxSession{live,
					{ID: "$2", Name: "work", Path: "/replacement", ServerGeneration: existingGeneration},
				}, lookupErr).Once()
			}
			success := state == "normal" || state == "renamed"
			if success {
				tm.EXPECT().SwitchOrAttach("$1", model.ConnectOpts{}, existingGeneration).Return("attached", nil).Once()
			}
			z := zoxide.NewMockZoxide(t)
			z.EXPECT().Add(row.Path).Return(nil).Once()
			// No lister expectations: the original display name cannot be resolved.
			c := connector.NewConnector(model.Config{}, nil, nil, lister.NewMockLister(t), nil, nil, tm, z, nil, nil)
			deps := &Deps{Picker: pk, Connector: c}
			cmd := newPickerCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			err := cmd.ExecuteContext(t.Context())
			if success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestPickerNonTmuxConnectKeepsNameResolution(t *testing.T) {
	for _, src := range []string{"zoxide", "dir", "config", "config_wildcard", "tmuxinator", "other"} {
		t.Run(src, func(t *testing.T) {
			// Non-local rows still resolve their name through the normal connector.
			pk := picker.NewMockPicker(t)
			pk.EXPECT().Pick(mock.Anything, mock.Anything).Return(model.SeshSession{Src: src, Name: "work"}, nil).Once()
			li := lister.NewMockLister(t)
			current := model.SeshSession{Src: "tmux", Name: "work", Path: "/current", TmuxSessionID: "$5", TmuxServerGeneration: existingGeneration}
			li.EXPECT().FindTmuxSession("work").Return(current, true).Once()
			tm := tmux.NewMockTmux(t)
			tm.EXPECT().IsAttached().Return(false).Once()
			tm.EXPECT().ListSessions().Return([]*model.TmuxSession{{ID: "$5", Name: "work", ServerGeneration: existingGeneration}}, nil).Once()
			tm.EXPECT().SwitchOrAttach("$5", model.ConnectOpts{}, existingGeneration).Return("attached", nil).Once()
			z := zoxide.NewMockZoxide(t)
			z.EXPECT().Add(current.Path).Return(nil).Once()
			deps := &Deps{Picker: pk, Connector: connector.NewConnector(model.Config{}, nil, nil, li, nil, nil, tm, z, nil, nil)}
			cmd := newPickerCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			require.NoError(t, cmd.ExecuteContext(t.Context()))
		})
	}
}

func TestPickerPaneConnectRetainsSelectedIdentity(t *testing.T) {
	row := model.SeshSession{
		Src: "tmux-pane", Name: "code/editor", Path: "/original",
		TmuxSessionID: "$1", TmuxWindowID: "@3", TmuxPaneID: "%7", TmuxServerGeneration: existingGeneration,
	}
	pk := picker.NewMockPicker(t)
	pk.EXPECT().Pick(mock.Anything, mock.Anything).Return(row, nil).Once()
	tm := tmux.NewMockTmux(t)
	tm.EXPECT().ListSessions().Return([]*model.TmuxSession{{ID: "$1", Name: "renamed", ServerGeneration: existingGeneration}}, nil).Once()
	original := &model.TmuxPane{
		SessionID: "$1", WindowID: "@3", PaneID: "%7", ServerGeneration: existingGeneration,
		WindowName: "renamed", PaneTitle: "changed", PanePath: "/changed",
	}
	tm.EXPECT().ListTmuxPanes().Return([]*model.TmuxPane{original,
		{SessionID: "$1", WindowID: "@4", PaneID: "%8", ServerGeneration: existingGeneration, WindowName: "code", PaneTitle: "editor", PanePath: row.Path},
	}, nil).Once()
	tm.EXPECT().SelectPane(original).Return("selected original", nil).Once()
	// Empty lister and zoxide mocks reject any name fallback or new side effect.
	c := connector.NewConnector(model.Config{}, nil, nil, lister.NewMockLister(t), nil, nil, tm, zoxide.NewMockZoxide(t), nil, nil)
	deps := &Deps{Picker: pk, Connector: c}
	cmd := newPickerCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
	require.NoError(t, cmd.ExecuteContext(t.Context()))
}
