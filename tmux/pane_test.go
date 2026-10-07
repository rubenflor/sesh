package tmux

import (
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListTmuxPanesPreservesOwningSessionMetadata(t *testing.T) {
	mockShell := shell.NewMockShell(t)
	mockShell.EXPECT().ListCmd("custom-tmux", "list-panes", "-s", "-F",
		"#{window_index}::#{window_name}::#{pane_index}::#{pane_title}::#{pane_current_command}::#{pane_current_path}::#{pane_id}::#{session_id}::#{window_id}").Return(
		[]string{
			"0::editor::1::local editor::vim::/p/repo::%1::$1::@1",
			"0::remote::0::bridge::ssh::/p/bridge::%2::$2::@2",
			"0::editor::2::local shell::zsh::/p/repo::%3::$1::@1",
			"0::remote::1::bridge shell::ssh::/p/bridge::%4::$2::@2",
			"malformed",
		}, nil).Once()
	mockShell.EXPECT().CmdCapture("custom-tmux", "show-environment", "-t", "$1", "--", "SESH_SSH_IDENTITY").Return("-SESH_SSH_IDENTITY", nil).Once()
	mockShell.EXPECT().CmdCapture("custom-tmux", "show-environment", "-t", "$2", "--", "SESH_SSH_IDENTITY").Return("SESH_SSH_IDENTITY=ssh/host/work", nil).Once()

	panes, err := NewTmux(nil, mockShell, "custom-tmux").ListTmuxPanes()

	require.NoError(t, err)
	require.Equal(t, []*model.TmuxPane{
		{WindowIndex: 0, WindowName: "editor", PaneIndex: 1, PaneTitle: "local editor", PaneCommand: "vim", PanePath: "/p/repo", PaneID: "%1", SessionID: "$1", WindowID: "@1"},
		{WindowIndex: 0, WindowName: "remote", PaneIndex: 0, PaneTitle: "bridge", PaneCommand: "ssh", PanePath: "/p/bridge", PaneID: "%2", SessionID: "$2", WindowID: "@2", SSHBridgeIdentity: "ssh/host/work"},
		{WindowIndex: 0, WindowName: "editor", PaneIndex: 2, PaneTitle: "local shell", PaneCommand: "zsh", PanePath: "/p/repo", PaneID: "%3", SessionID: "$1", WindowID: "@1"},
		{WindowIndex: 0, WindowName: "remote", PaneIndex: 1, PaneTitle: "bridge shell", PaneCommand: "ssh", PanePath: "/p/bridge", PaneID: "%4", SessionID: "$2", WindowID: "@2", SSHBridgeIdentity: "ssh/host/work"},
	}, panes)
}

func TestListTmuxPanesPropagatesBridgeMarkerFailure(t *testing.T) {
	mockShell := shell.NewMockShell(t)
	wantErr := errors.New("session environment unavailable")
	mockShell.EXPECT().ListCmd("custom-tmux", "list-panes", "-s", "-F",
		"#{window_index}::#{window_name}::#{pane_index}::#{pane_title}::#{pane_current_command}::#{pane_current_path}::#{pane_id}::#{session_id}::#{window_id}").Return(
		[]string{"0::editor::1::local editor::vim::/p/repo::%1::$1::@1"}, nil).Once()
	mockShell.EXPECT().CmdCapture("custom-tmux", "show-environment", "-t", "$1", "--", "SESH_SSH_IDENTITY").Return("", wantErr).Once()

	panes, err := NewTmux(nil, mockShell, "custom-tmux").ListTmuxPanes()

	require.Nil(t, panes)
	require.ErrorIs(t, err, wantErr)
	require.ErrorContains(t, err, `SSH bridge marker for pane "%1" in session "$1"`)
}

func TestSelectPaneUsesVerifiedIDs(t *testing.T) {
	for _, fail := range []string{"", "select-window", "select-pane"} {
		t.Run("failure="+fail, func(t *testing.T) {
			mockShell := shell.NewMockShell(t)
			pane := &model.TmuxPane{SessionID: "$1", WindowID: "@3", PaneID: "%5", WindowIndex: 1, PaneIndex: 1}
			wantErr := errors.New("selection failed")
			var windowErr, paneErr error
			if fail == "select-window" {
				windowErr = wantErr
			} else if fail == "select-pane" {
				paneErr = wantErr
			}
			mockShell.EXPECT().Cmd("custom-tmux", "select-window", "-t", "$1:@3").Return("", windowErr).Once()
			if windowErr == nil {
				mockShell.EXPECT().Cmd("custom-tmux", "select-pane", "-t", "%5").Return("", paneErr).Once()
			}

			message, err := NewTmux(nil, mockShell, "custom-tmux").SelectPane(pane)

			if fail != "" {
				require.ErrorIs(t, err, wantErr)
				require.Empty(t, message)
			} else {
				require.NoError(t, err)
				require.Equal(t, "selected pane 1 in window 1", message)
			}
		})
	}
}

func TestSelectPaneRejectsMissingIDs(t *testing.T) {
	for _, pane := range []*model.TmuxPane{
		{WindowID: "@3", PaneID: "%5"},
		{SessionID: "$1", PaneID: "%5"},
		{SessionID: "$1", WindowID: "@3"},
	} {
		message, err := NewTmux(nil, nil, "custom-tmux").SelectPane(pane)
		require.EqualError(t, err, "pane has no server-assigned session, window or pane ID")
		require.Empty(t, message)
	}
}

func TestGetCurrentSessionVerifiesIDAndGeneration(t *testing.T) {
	const generation = "101:1700000000:/tmp/tmux-fixture"
	const format = "#{session_id}::" + serverGenerationFormat
	for _, tt := range []struct {
		name       string
		initial    string
		initialErr error
		marker     string
		markerErr  error
		live       string
		liveErr    error
		wantErr    bool
	}{
		{name: "local session", initial: "$1::" + generation, live: "$1::" + generation},
		{name: "bridge ownership is retained", initial: "$1::" + generation, marker: "ssh/host/work", live: "$1::" + generation},
		{name: "query failure", initialErr: assert.AnError, wantErr: true},
		{name: "empty identity", wantErr: true},
		{name: "display name is not an ID", initial: "name::" + generation, wantErr: true},
		{name: "noncanonical ID", initial: "$01::" + generation, wantErr: true},
		{name: "missing generation", initial: "$1", wantErr: true},
		{name: "unknown generation", initial: "$1::", wantErr: true},
		{name: "invalid generation", initial: "$1::101:0:/tmp/tmux-fixture", wantErr: true},
		{name: "unexpanded generation", initial: "$1::" + serverGenerationFormat, wantErr: true},
		{name: "disappeared before ownership", initial: "$1::" + generation, markerErr: assert.AnError, wantErr: true},
		{name: "disappeared after ownership", initial: "$1::" + generation, liveErr: assert.AnError, wantErr: true},
		{name: "empty live identity", initial: "$1::" + generation, wantErr: true},
		{name: "namesake must not substitute ID", initial: "$1::" + generation, live: "$2::" + generation, wantErr: true},
		{name: "restart after ownership", initial: "$1::" + generation, live: "$1::102:1700000001:/tmp/tmux-fixture", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := shell.NewMockShell(t)
			query := s.EXPECT().Cmd("custom-tmux", "display-message", "-p", format).Return(tt.initial, tt.initialErr).Once()
			if tt.initial == "$1::"+generation {
				marker := s.EXPECT().CmdCapture("custom-tmux", "show-environment", "-t", "$1", "--", model.SSHBridgeIdentityEnv).
					Return(model.SSHBridgeIdentityEnv+"="+tt.marker, tt.markerErr).Once().NotBefore(query)
				if tt.markerErr == nil {
					s.EXPECT().Cmd("custom-tmux", "display-message", "-p", "-t", "$1:", format).
						Return(tt.live, tt.liveErr).Once().NotBefore(marker)
				}
			}

			current, err := NewTmux(nil, s, "custom-tmux").GetCurrentSession()

			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, model.TmuxSession{}, current)
			} else {
				require.NoError(t, err)
				require.Equal(t, model.TmuxSession{ID: "$1", ServerGeneration: generation, SSHBridgeIdentity: tt.marker}, current)
			}
		})
	}
}
