package connector

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectPaneRejectsMarkedRawPane(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().IsAttached().Return(true).Once()
	mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{{ID: "$1", ServerGeneration: existingSessionGeneration}}, nil).Once()
	mTmux.EXPECT().ListTmuxPanes().Return([]*model.TmuxPane{
		{SessionID: "$1", WindowID: "@3", PaneID: "%7", ServerGeneration: existingSessionGeneration, WindowName: "code", SSHBridgeIdentity: "ssh/host/0"},
	}, nil).Once()
	mLister := lister.NewMockLister(t)
	// A stale public selection must not override the raw owning-session marker.
	mLister.EXPECT().ListTmuxPanes().Return(model.SeshSessions{
		OrderedIndex: []string{"tmux-pane:code/%7"},
		Directory: model.SeshSessionMap{
			"tmux-pane:code/%7": {Src: "tmux-pane", Name: "code/editor", TmuxSessionID: "$1", TmuxWindowID: "@3", TmuxPaneID: "%7", TmuxServerGeneration: existingSessionGeneration},
		},
	}, nil).Once()
	c := NewConnector(model.Config{}, nil, nil, mLister, nil, nil, mTmux, nil, nil, nil)

	result, err := c.Connect("code/editor", model.ConnectOpts{})

	require.EqualError(t, err, "pane not found: code/editor")
	require.Empty(t, result)
}

func TestConnectPanePreservesSelectedIdentity(t *testing.T) {
	for _, handoff := range []string{"picker", "resolved name"} {
		for _, change := range []string{"normal", "display takeover"} {
			t.Run(handoff+"/"+change, func(t *testing.T) {
				selected := model.SeshSession{
					Src: "tmux-pane", Name: "code/editor", Path: "/original",
					TmuxSessionID: "$1", TmuxWindowID: "@3", TmuxPaneID: "%7", TmuxServerGeneration: existingSessionGeneration,
				}
				original := &model.TmuxPane{
					SessionID: "$1", WindowID: "@3", PaneID: "%7", ServerGeneration: existingSessionGeneration,
					WindowName: "code", PaneTitle: "editor", PanePath: "/original",
				}
				panes := []*model.TmuxPane{original}
				if change == "display takeover" {
					original.WindowName, original.PaneTitle, original.PanePath = "renamed", "changed", "/changed"
					panes = append(panes, &model.TmuxPane{
						SessionID: "$1", WindowID: "@4", PaneID: "%8", ServerGeneration: existingSessionGeneration,
						WindowName: "code", PaneTitle: "editor", PanePath: selected.Path,
					})
				}
				tm := tmux.NewMockTmux(t)
				li := lister.NewMockLister(t)
				c := &RealConnector{tmux: tm, lister: li}
				connection := model.Connection{Session: selected}
				if handoff == "resolved name" {
					tm.EXPECT().IsAttached().Return(true).Once()
					li.EXPECT().ListTmuxPanes().Return(model.SeshSessions{
						OrderedIndex: []string{"selected"}, Directory: model.SeshSessionMap{"selected": selected},
					}, nil).Once()
					var err error
					connection, err = c.Resolve(selected.Name)
					require.NoError(t, err)
					require.Equal(t, selected, connection.Session)
				}
				tm.EXPECT().ListSessions().Return([]*model.TmuxSession{
					{ID: "$0", SSHBridgeIdentity: "ssh/host/work", ServerGeneration: existingSessionGeneration},
					{ID: "$1", Name: "$0", ServerGeneration: existingSessionGeneration},
				}, nil).Once()
				tm.EXPECT().ListTmuxPanes().Return(panes, nil).Once()
				tm.EXPECT().SelectPane(original).Return("selected original", nil).Once()
				var message string
				var err error
				if handoff == "picker" {
					message, err = c.ConnectSession(selected, model.ConnectOpts{})
				} else {
					message, err = connectToTmuxPane(c, connection, model.ConnectOpts{})
				}
				require.NoError(t, err)
				require.Equal(t, "selected original", message)
			})
		}
	}
}

func TestConnectPaneFailsClosed(t *testing.T) {
	for _, change := range []string{
		"missing session", "missing window", "missing pane", "missing generation", "unknown generation",
		"disappeared", "moved window", "moved session", "marked pane", "marked session", "restarted",
		"generation changed during inventory", "session inventory error", "pane inventory error", "selection error",
	} {
		t.Run(change, func(t *testing.T) {
			selected := model.SeshSession{
				Src: "tmux-pane", Name: "code/editor", Path: "/original",
				TmuxSessionID: "$1", TmuxWindowID: "@3", TmuxPaneID: "%7", TmuxServerGeneration: existingSessionGeneration,
			}
			session := &model.TmuxSession{ID: "$1", ServerGeneration: existingSessionGeneration}
			original := &model.TmuxPane{SessionID: "$1", WindowID: "@3", PaneID: "%7", ServerGeneration: existingSessionGeneration}
			replacement := &model.TmuxPane{SessionID: "$1", WindowID: "@3", PaneID: "%8", ServerGeneration: existingSessionGeneration, WindowName: "code", PaneTitle: "editor", PanePath: selected.Path}
			panes := []*model.TmuxPane{original, replacement}
			switch change {
			case "missing session":
				selected.TmuxSessionID = ""
			case "missing window":
				selected.TmuxWindowID = ""
			case "missing pane":
				selected.TmuxPaneID = ""
			case "missing generation":
				selected.TmuxServerGeneration = ""
			case "unknown generation":
				selected.TmuxServerGeneration = "unknown"
			case "disappeared":
				panes = []*model.TmuxPane{replacement}
			case "moved window":
				original.WindowID = "@4"
			case "moved session":
				original.SessionID = "$2"
			case "marked pane":
				original.SSHBridgeIdentity = "ssh/host/work"
			case "marked session":
				session.SSHBridgeIdentity = "ssh/host/work"
			case "restarted":
				session.ServerGeneration = "102:1700000001:/tmp/tmux-fixture"
			case "generation changed during inventory":
				original.ServerGeneration = "102:1700000001:/tmp/tmux-fixture"
			}
			tm := tmux.NewMockTmux(t)
			if selected.TmuxSessionID != "" && selected.TmuxWindowID != "" && selected.TmuxPaneID != "" &&
				selected.TmuxServerGeneration != "" && selected.TmuxServerGeneration != "unknown" {
				var sessionErr error
				if change == "session inventory error" {
					sessionErr = assert.AnError
				}
				tm.EXPECT().ListSessions().Return([]*model.TmuxSession{session}, sessionErr).Once()
				if sessionErr == nil && change != "restarted" && change != "marked session" {
					var paneErr error
					if change == "pane inventory error" {
						paneErr = assert.AnError
					}
					tm.EXPECT().ListTmuxPanes().Return(panes, paneErr).Once()
				}
			}
			if change == "selection error" {
				tm.EXPECT().SelectPane(original).Return("", assert.AnError).Once()
			}
			// No lister/name resolution or replacement selection is allowed.
			c := &RealConnector{tmux: tm, lister: lister.NewMockLister(t)}
			message, err := c.ConnectSession(selected, model.ConnectOpts{})
			require.Error(t, err)
			require.Empty(t, message)
			if change == "session inventory error" || change == "pane inventory error" || change == "selection error" {
				require.ErrorIs(t, err, assert.AnError)
			}
		})
	}
}
