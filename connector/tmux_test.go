package connector

import (
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/startup"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	mock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestEstablishTmuxConnection(t *testing.T) {
	mockDir := new(dir.MockDir)
	mockHome := new(home.MockHome)
	mockLister := new(lister.MockLister)
	mockNamer := new(namer.MockNamer)
	mockStartup := new(startup.MockStartup)
	mockTmux := new(tmux.MockTmux)
	mockZoxide := new(zoxide.MockZoxide)
	mockTmuxinator := new(tmuxinator.MockTmuxinator)

	c := &RealConnector{
		model.Config{},
		mockDir,
		mockHome,
		mockLister,
		mockNamer,
		mockStartup,
		mockTmux,
		mockZoxide,
		mockTmuxinator,
		nil,
	}
	mockTmux.On("AttachSession", mock.Anything).Return("attaching", nil)
	mockZoxide.On("Add", mock.Anything).Return(nil)

	t.Run("should attach to tmux session", func(t *testing.T) {
		mockTmux.On("IsAttached").Return(false)
		mockLister.On("FindTmuxSession", "dotfiles").Return(model.SeshSession{
			Name: "dotfiles",
			Path: "/Users/joshmedeski/c/dotfiles",
		}, true)
		connection, err := tmuxStrategy(c, "dotfiles")
		assert.Nil(t, err)
		assert.Equal(t, "dotfiles", connection.Session.Name)
	})

	t.Run("should switch to tmux session", func(t *testing.T) {
		mockTmux.On("IsAttached").Return(true)
		mockLister.On("FindTmuxSession", "dotfiles").Return(model.SeshSession{
			Name: "dotfiles",
			Path: "/Users/joshmedeski/c/dotfiles",
		}, true)
		connection, err := tmuxStrategy(c, "dotfiles")
		assert.Nil(t, err)
		assert.Equal(t, "dotfiles", connection.Session.Name)
	})
}

func TestConnectToTmuxDetachedSwitchesClientAndFocuses(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mFocuser := focuser.NewMockFocuser(t)

	// New session path
	mTmux.EXPECT().NewSession("nutiliti/2345", "/repo/w/2345").Return("", nil)
	// No opts.Command => startup.Exec is called; startup is a no-op mock here
	mStartup := startup.NewMockStartup(t)
	mStartup.EXPECT().Exec(mock.Anything).Return("", nil).Maybe()

	// Detached: not attached, --switch set
	mTmux.EXPECT().IsAttached().Return(false)
	mTmux.EXPECT().ListClients().Return([]model.TmuxClient{
		{Name: "/dev/ttys002", TTY: "/dev/ttys002", SessionID: "$2"},
	}, nil)
	mTmux.EXPECT().ResolveClient().Return("/dev/ttys002")
	mTmux.EXPECT().SwitchClientTarget("/dev/ttys002", "nutiliti/2345").Return("", nil)
	mFocuser.EXPECT().Activate("wezterm").Return(true, nil)

	c := NewConnector(
		model.Config{Terminal: "wezterm"},
		nil, nil, nil, nil, mStartup, mTmux, nil, nil, mFocuser,
	).(*RealConnector)

	conn := model.Connection{
		Found: true, New: true,
		Session: model.SeshSession{Src: "dir", Name: "nutiliti/2345", Path: "/repo/w/2345"},
	}
	_, err := connectToTmux(c, conn, model.ConnectOpts{Switch: true})
	require.NoError(t, err)
}

func TestConnectToTmuxDetachedWithNoClientStillFocuses(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mFocuser := focuser.NewMockFocuser(t)

	// Nothing is attached to the server, so there is no client to switch.
	mTmux.EXPECT().IsAttached().Return(false)
	mTmux.EXPECT().ListClients().Return(nil, nil)
	mFocuser.EXPECT().Activate("wezterm").Return(true, nil)

	c := NewConnector(
		model.Config{Terminal: "wezterm"},
		nil, nil, nil, nil, nil, mTmux, nil, nil, mFocuser,
	).(*RealConnector)

	conn := model.Connection{
		Found: true, New: false,
		Session: model.SeshSession{Src: "tmux", Name: "nutiliti/2345", Path: "/repo/w/2345"},
	}
	msg, err := connectToTmux(c, conn, model.ConnectOpts{Switch: true})
	require.NoError(t, err)
	assert.Equal(t, "connected to tmux session: nutiliti/2345", msg)
}

// A GUI launcher runs sesh with a bare PATH, so tmux isn't reachable while
// Activate's osascript still is. That used to focus the terminal, change
// nothing, and report success.
func TestConnectToTmuxDetachedReportsUnreachableTmux(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mFocuser := focuser.NewMockFocuser(t)

	mTmux.EXPECT().IsAttached().Return(false)
	mTmux.EXPECT().ListClients().
		Return(nil, errors.New(`exec: "tmux": executable file not found in $PATH`))

	c := NewConnector(
		model.Config{Terminal: "wezterm"},
		nil, nil, nil, nil, nil, mTmux, nil, nil, mFocuser,
	).(*RealConnector)

	conn := model.Connection{
		Found: true, New: false,
		Session: model.SeshSession{Src: "tmux", Name: "nutiliti/2345", Path: "/repo/w/2345"},
	}
	_, err := connectToTmux(c, conn, model.ConnectOpts{Switch: true})
	require.ErrorContains(t, err, "couldn't reach tmux to switch to 'nutiliti/2345'")
}

func TestConnectSSHCreatesBridgeAndAttaches(t *testing.T) {
	const identity = "ssh/host/0"
	const bridge = "sesh-ssh-71733fb26ad99c67e0bf6bbe-0"
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return(nil, nil).Once()
	mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
		Name:        bridge,
		Command:     `exec ssh -tt -- 'host' 'tmux attach-session -t '\''=0'\'''`,
		Environment: []string{"SESH_SSH_IDENTITY=ssh/host/0"},
	}).Return("", nil).Once()
	mTmux.EXPECT().SwitchOrAttach(bridge, model.ConnectOpts{}).
		Return("attaching to tmux session: "+bridge, nil).Once()
	c := NewConnector(model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}},
		dir.NewMockDir(t), home.NewMockHome(t), lister.NewMockLister(t), namer.NewMockNamer(t),
		startup.NewMockStartup(t), mTmux, zoxide.NewMockZoxide(t),
		tmuxinator.NewMockTmuxinator(t), focuser.NewMockFocuser(t),
	)

	response, err := c.Connect(identity, model.ConnectOpts{})

	require.NoError(t, err)
	assert.Equal(t, "connected to SSH session: "+identity, response)
}

func TestConnectSSHSwitchChecksClientBeforeBridgeAccess(t *testing.T) {
	for _, tt := range []struct {
		name      string
		clientErr error
		wantError string
	}{
		{"no attached client", nil, "no attached local tmux client"},
		{"unreachable tmux", errors.New("tmux unavailable"), "tmux unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mTmux := tmux.NewMockTmux(t)
			mTmux.EXPECT().ListClients().Return(nil, tt.clientErr).Once()
			c := NewConnector(model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}},
				dir.NewMockDir(t), home.NewMockHome(t), lister.NewMockLister(t), namer.NewMockNamer(t),
				startup.NewMockStartup(t), mTmux, zoxide.NewMockZoxide(t),
				tmuxinator.NewMockTmuxinator(t), focuser.NewMockFocuser(t),
			)

			response, err := c.Connect("ssh/host/0", model.ConnectOpts{Switch: true})

			require.ErrorContains(t, err, tt.wantError)
			assert.Empty(t, response)
			if tt.clientErr == nil {
				assert.Contains(t, err.Error(), "without --switch")
			}
		})
	}
}

func TestConnectSSHReusesBridge(t *testing.T) {
	for _, tt := range []struct {
		name     string
		switchTo bool
		attached bool
	}{
		{"plain connect", false, false},
		{"switch inside tmux", true, true},
		{"switch outside tmux", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const identity = "ssh/host/0"
			const bridge = "renamed bridge"
			mTmux := tmux.NewMockTmux(t)
			mFocuser := focuser.NewMockFocuser(t)
			mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{{Name: bridge, ID: "$8"}}, nil).Once()
			mTmux.EXPECT().GetSessionEnvironment("$8", model.SSHBridgeIdentityEnv).Return(identity, nil).Once()
			if tt.switchTo {
				clientChecks := 1
				if !tt.attached {
					clientChecks++
				}
				mTmux.EXPECT().ListClients().Return([]model.TmuxClient{{Name: "/dev/ttys002"}}, nil).Times(clientChecks)
				mTmux.EXPECT().IsAttached().Return(tt.attached).Once()
			}
			if tt.switchTo && !tt.attached {
				mTmux.EXPECT().ResolveClient().Return("/dev/ttys002").Once()
				mTmux.EXPECT().SwitchClientTarget("/dev/ttys002", bridge).Return("", nil).Once()
				mFocuser.EXPECT().Activate("wezterm").Return(true, nil).Once()
			} else {
				mTmux.EXPECT().SwitchOrAttach(bridge, model.ConnectOpts{Switch: tt.switchTo}).Return("", nil).Once()
			}
			c := NewConnector(model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}, Terminal: "wezterm"},
				dir.NewMockDir(t), home.NewMockHome(t), lister.NewMockLister(t), namer.NewMockNamer(t),
				startup.NewMockStartup(t), mTmux, zoxide.NewMockZoxide(t), tmuxinator.NewMockTmuxinator(t), mFocuser,
			)

			response, err := c.Connect(identity, model.ConnectOpts{Switch: tt.switchTo})

			require.NoError(t, err)
			assert.Equal(t, "connected to SSH session: "+identity, response)
		})
	}
}

func TestConnectSSHReportsAttachFailure(t *testing.T) {
	const identity = "ssh/host/0"
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{{Name: "bridge", ID: "$8"}}, nil).Once()
	mTmux.EXPECT().GetSessionEnvironment("$8", model.SSHBridgeIdentityEnv).Return(identity, nil).Once()
	wantErr := errors.New("terminal unavailable")
	mTmux.EXPECT().SwitchOrAttach("bridge", model.ConnectOpts{}).Return("", wantErr).Once()
	c := NewConnector(model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}},
		dir.NewMockDir(t), home.NewMockHome(t), lister.NewMockLister(t), namer.NewMockNamer(t),
		startup.NewMockStartup(t), mTmux, zoxide.NewMockZoxide(t),
		tmuxinator.NewMockTmuxinator(t), focuser.NewMockFocuser(t),
	)

	response, err := c.Connect(identity, model.ConnectOpts{})

	require.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), identity)
	assert.Empty(t, response)
}
