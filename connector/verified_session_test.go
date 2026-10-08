package connector

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSHConnectReusesVerifiedBridgeID(t *testing.T) {
	for _, name := range []string{"$0", "$1", "$2", "renamed bridge"} {
		for _, switchFlag := range []bool{false, true} {
			t.Run(name+"/"+map[bool]string{false: "attach", true: "switch"}[switchFlag], func(t *testing.T) {
				m := tmux.NewMockTmux(t)
				if switchFlag {
					m.EXPECT().ListClients().Return([]model.TmuxClient{{Name: "client"}}, nil).Once()
				}
				m.EXPECT().ListSessions().Return([]*model.TmuxSession{
					{Name: "unrelated", ID: "$0"},
					{Name: name, ID: "$8"},
				}, nil).Once()
				m.EXPECT().GetSessionEnvironment("$0", model.SSHBridgeIdentityEnv).Return("", nil).Once()
				m.EXPECT().GetSessionEnvironment("$8", model.SSHBridgeIdentityEnv).Return("ssh/host/work", nil).Once()
				m.EXPECT().IsPaneDead("$8").Return(false, nil).Once()
				if switchFlag {
					m.EXPECT().IsAttached().Return(true)
				}
				m.EXPECT().SwitchOrAttach("$8", model.ConnectOpts{Switch: switchFlag}).Return("", nil).Once()
				c := &RealConnector{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}}, tmux: m}

				message, err := c.Connect("ssh/host/work", model.ConnectOpts{Switch: switchFlag})

				require.NoError(t, err)
				assert.Equal(t, "connected to SSH session: ssh/host/work", message)
			})
		}
	}
}

func TestWindowConnectUsesVerifiedSessionID(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "reuse"}[reuse], func(t *testing.T) {
			m := tmux.NewMockTmux(t)
			li := lister.NewMockLister(t)
			zo := zoxide.NewMockZoxide(t)
			m.EXPECT().IsAttached().Return(true)
			li.EXPECT().FindTmuxSession("$2").Return(model.SeshSession{Src: "tmux", Name: "$2", Path: "/user", TmuxSessionID: "$8", TmuxServerGeneration: existingSessionGeneration}, true)
			zo.EXPECT().Add("/user").Return(nil)
			m.EXPECT().ListSessions().Return([]*model.TmuxSession{
				{Name: "$2", ID: "$8", ServerGeneration: existingSessionGeneration, Path: "/user"},
				{Name: "bridge", ID: "$2", SSHBridgeIdentity: "ssh/host/work"},
			}, nil).Once()
			var windows []*model.TmuxWindow
			focusTarget := "$8"
			if reuse {
				windows = []*model.TmuxWindow{{ID: "@7", ServerGeneration: existingSessionGeneration, Name: "notes", Index: 4}}
				m.EXPECT().SendKeys("$8:@7", "command", existingSessionGeneration).Return("", nil).Once()
				m.EXPECT().SelectWindow("$8:@7", existingSessionGeneration).Return("", nil).Once()
				focusTarget = "$8:@7"
			} else {
				m.EXPECT().NewWindowInSession(model.TmuxWindowOpts{Name: "notes", StartDir: "/user", TargetSession: "$8", Command: "command"}, existingSessionGeneration).Return("$2:4", nil).Once()
			}
			m.EXPECT().ListWindows("$8", existingSessionGeneration).Return(windows, nil).Once()
			m.EXPECT().SwitchOrAttach(focusTarget, model.ConnectOpts{}, existingSessionGeneration).Return("", nil).Once()
			c := &RealConnector{tmux: m, lister: li, zoxide: zo}

			target, err := c.ConnectWindow(model.WindowConnectOpts{Session: "$2", Name: "notes", Path: "/user", Command: "command"})

			require.NoError(t, err)
			assert.Equal(t, "$2:4", target)
		})
	}
}
