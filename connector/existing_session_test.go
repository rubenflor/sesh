package connector

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const existingSessionGeneration = "101:1700000000:/tmp/tmux-fixture"

func TestExistingSessionConnectKeepsCapturedIdentity(t *testing.T) {
	for _, mode := range []string{"attach", "switch", "detached client", "detached fallback"} {
		t.Run(mode, func(t *testing.T) {
			m := tmux.NewMockTmux(t)
			m.EXPECT().ListSessions().Return([]*model.TmuxSession{
				{ID: "$0", Name: "bridge", ServerGeneration: existingSessionGeneration, SSHBridgeIdentity: "ssh/host/work"},
				{ID: "$1", Name: "renamed", ServerGeneration: existingSessionGeneration, Path: "/original"},
				{ID: "$2", Name: "$0", ServerGeneration: existingSessionGeneration, Path: "/replacement"},
			}, nil).Once()
			c := &RealConnector{tmux: m}
			switchFlag := mode != "attach"
			if mode == "switch" {
				m.EXPECT().IsAttached().Return(true).Once()
			}
			if mode == "attach" || mode == "switch" {
				m.EXPECT().SwitchOrAttach("$1", model.ConnectOpts{Switch: switchFlag}, existingSessionGeneration).Return("connected to $1", nil).Once()
			} else {
				m.EXPECT().IsAttached().Return(false).Once()
				m.EXPECT().ListClients().Return([]model.TmuxClient{{Name: "client"}}, nil).Once()
				client := "client"
				if mode == "detached fallback" {
					client = ""
					m.EXPECT().SwitchClient("$1", existingSessionGeneration).Return("", nil).Once()
				} else {
					m.EXPECT().SwitchClientTarget(client, "$1", existingSessionGeneration).Return("", nil).Once()
				}
				m.EXPECT().ResolveClient().Return(client).Once()
				f := focuser.NewMockFocuser(t)
				f.EXPECT().Activate("").Return(true, nil).Once()
				c.focuser = f
			}
			_, err := connectToTmux(c, model.Connection{Session: model.SeshSession{
				Src: "tmux", Name: "$0", Path: "/original", TmuxSessionID: "$1", TmuxServerGeneration: existingSessionGeneration,
			}}, model.ConnectOpts{Switch: switchFlag})
			require.NoError(t, err)
		})
	}
}

func TestExistingWindowSessionKeepsIdentityAndPath(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit"}[explicit], func(t *testing.T) {
			m := tmux.NewMockTmux(t)
			li := lister.NewMockLister(t)
			selected := model.SeshSession{Src: "tmux", Name: "$0", Path: "/old-metadata", TmuxSessionID: "$1", TmuxServerGeneration: existingSessionGeneration}
			name := ""
			c := &RealConnector{tmux: m, lister: li}
			if explicit {
				name = "$0"
				m.EXPECT().IsAttached().Return(false).Once()
				li.EXPECT().FindTmuxSession(name).Return(selected, true).Once()
				z := zoxide.NewMockZoxide(t)
				z.EXPECT().Add(selected.Path).Return(nil).Once()
				c.zoxide = z
			} else {
				li.EXPECT().GetAttachedTmuxSession().Return(selected, true).Once()
			}
			m.EXPECT().ListSessions().Return([]*model.TmuxSession{
				{ID: "$0", Name: "bridge", ServerGeneration: existingSessionGeneration, SSHBridgeIdentity: "ssh/host/work"},
				{ID: "$1", Name: "renamed", Path: "/original", ServerGeneration: existingSessionGeneration},
				{ID: "$2", Name: "$0", Path: "/replacement", ServerGeneration: existingSessionGeneration},
			}, nil).Once()
			m.EXPECT().NewWindowInSession(model.TmuxWindowOpts{TargetSession: "$1", StartDir: "/original", Background: true}, existingSessionGeneration).Return("renamed:1", nil).Once()

			target, err := c.ConnectWindow(model.WindowConnectOpts{Session: name, Background: true})

			require.NoError(t, err)
			require.Equal(t, "renamed:1", target)
		})
	}
}

func TestExistingSessionActionsFailClosed(t *testing.T) {
	for _, invalid := range []string{"missing ID", "missing generation", "disappeared", "restarted", "marked", "lookup error"} {
		for _, action := range []string{"connect", "default window", "explicit window"} {
			t.Run(invalid+"/"+action, func(t *testing.T) {
				m := tmux.NewMockTmux(t)
				selected := model.SeshSession{Src: "tmux", Name: "$0", Path: "/original", TmuxSessionID: "$1", TmuxServerGeneration: existingSessionGeneration}
				live := &model.TmuxSession{ID: "$1", Name: "renamed", ServerGeneration: existingSessionGeneration}
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
					m.EXPECT().ListSessions().Return([]*model.TmuxSession{live, {ID: "$2", Name: "$0", ServerGeneration: existingSessionGeneration}}, lookupErr).Once()
				}
				c := &RealConnector{tmux: m}
				var err error
				if action == "connect" {
					_, err = connectToTmux(c, model.Connection{Session: selected}, model.ConnectOpts{})
				} else {
					li := lister.NewMockLister(t)
					c.lister = li
					name := ""
					if action == "default window" {
						li.EXPECT().GetAttachedTmuxSession().Return(selected, true).Once()
					} else {
						name = "$0"
						m.EXPECT().IsAttached().Return(false).Once()
						li.EXPECT().FindTmuxSession(name).Return(selected, true).Once()
						z := zoxide.NewMockZoxide(t)
						z.EXPECT().Add(selected.Path).Return(nil).Once()
						c.zoxide = z
					}
					_, err = c.ConnectWindow(model.WindowConnectOpts{Session: name})
				}
				require.Error(t, err)
				m.AssertNotCalled(t, "SwitchOrAttach", mock.Anything, mock.Anything, mock.Anything)
				m.AssertNotCalled(t, "NewWindowInSession", mock.Anything, mock.Anything)
			})
		}
	}
}
