package connector

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

func TestConnectPaneRejectsMarkedRawPane(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().IsAttached().Return(true).Once()
	mTmux.EXPECT().ListTmuxPanes().Return([]*model.TmuxPane{
		{WindowName: "code", PaneID: "%3", SSHBridgeIdentity: "ssh/host/0"},
	}, nil).Once()
	mLister := lister.NewMockLister(t)
	// A stale public selection must not override the raw owning-session marker.
	mLister.EXPECT().ListTmuxPanes().Return(model.SeshSessions{
		OrderedIndex: []string{"tmux-pane:code/%3"},
		Directory: model.SeshSessionMap{
			"tmux-pane:code/%3": {Src: "tmux-pane", Name: "code/editor"},
		},
	}, nil).Twice()
	c := NewConnector(model.Config{}, nil, nil, mLister, nil, nil, mTmux, nil, nil, nil)

	result, err := c.Connect("code/editor", model.ConnectOpts{})

	require.EqualError(t, err, "pane not found: code/editor")
	require.Empty(t, result)
}
