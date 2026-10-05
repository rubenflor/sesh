package tmux

import (
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/require"
)

func TestListTmuxPanesPreservesOwningSessionMetadata(t *testing.T) {
	mockShell := shell.NewMockShell(t)
	mockShell.EXPECT().ListCmd("custom-tmux", "list-panes", "-s", "-F",
		"#{window_index}::#{window_name}::#{pane_index}::#{pane_title}::#{pane_current_command}::#{pane_current_path}::#{pane_id}::#{session_id}").Return(
		[]string{
			"0::editor::1::local editor::vim::/p/repo::%1::$1",
			"0::remote::0::bridge::ssh::/p/bridge::%2::$2",
			"0::editor::2::local shell::zsh::/p/repo::%3::$1",
			"0::remote::1::bridge shell::ssh::/p/bridge::%4::$2",
			"malformed",
		}, nil).Once()
	mockShell.EXPECT().CmdCapture("custom-tmux", "show-environment", "-t", "$1", "--", "SESH_SSH_IDENTITY").Return("-SESH_SSH_IDENTITY", nil).Once()
	mockShell.EXPECT().CmdCapture("custom-tmux", "show-environment", "-t", "$2", "--", "SESH_SSH_IDENTITY").Return("SESH_SSH_IDENTITY=ssh/host/work", nil).Once()

	panes, err := NewTmux(nil, mockShell, "custom-tmux").ListTmuxPanes()

	require.NoError(t, err)
	require.Equal(t, []*model.TmuxPane{
		{WindowIndex: 0, WindowName: "editor", PaneIndex: 1, PaneTitle: "local editor", PaneCommand: "vim", PanePath: "/p/repo", PaneID: "%1", SessionID: "$1"},
		{WindowIndex: 0, WindowName: "remote", PaneIndex: 0, PaneTitle: "bridge", PaneCommand: "ssh", PanePath: "/p/bridge", PaneID: "%2", SessionID: "$2", SSHBridgeIdentity: "ssh/host/work"},
		{WindowIndex: 0, WindowName: "editor", PaneIndex: 2, PaneTitle: "local shell", PaneCommand: "zsh", PanePath: "/p/repo", PaneID: "%3", SessionID: "$1"},
		{WindowIndex: 0, WindowName: "remote", PaneIndex: 1, PaneTitle: "bridge shell", PaneCommand: "ssh", PanePath: "/p/bridge", PaneID: "%4", SessionID: "$2", SSHBridgeIdentity: "ssh/host/work"},
	}, panes)
}

func TestListTmuxPanesPropagatesBridgeMarkerFailure(t *testing.T) {
	mockShell := shell.NewMockShell(t)
	wantErr := errors.New("session environment unavailable")
	mockShell.EXPECT().ListCmd("custom-tmux", "list-panes", "-s", "-F",
		"#{window_index}::#{window_name}::#{pane_index}::#{pane_title}::#{pane_current_command}::#{pane_current_path}::#{pane_id}::#{session_id}").Return(
		[]string{"0::editor::1::local editor::vim::/p/repo::%1::$1"}, nil).Once()
	mockShell.EXPECT().CmdCapture("custom-tmux", "show-environment", "-t", "$1", "--", "SESH_SSH_IDENTITY").Return("", wantErr).Once()

	panes, err := NewTmux(nil, mockShell, "custom-tmux").ListTmuxPanes()

	require.Nil(t, panes)
	require.ErrorIs(t, err, wantErr)
	require.ErrorContains(t, err, `SSH bridge marker for pane "%1" in session "$1"`)
}
