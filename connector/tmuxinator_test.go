package connector

import (
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/startup"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newConnectorWithMocks(t *testing.T) (*RealConnector, *tmuxinator.MockTmuxinator, *tmux.MockTmux) {
	mockTmuxinator := tmuxinator.NewMockTmuxinator(t)
	mockTmux := tmux.NewMockTmux(t)
	c := &RealConnector{
		model.Config{},
		new(dir.MockDir),
		new(home.MockHome),
		new(lister.MockLister),
		new(namer.MockNamer),
		new(startup.MockStartup),
		mockTmux,
		new(zoxide.MockZoxide),
		mockTmuxinator,
		nil,
	}
	return c, mockTmuxinator, mockTmux
}

func TestConnectToTmuxinator(t *testing.T) {
	t.Run("propagates error from Start and does not attach", func(t *testing.T) {
		c, mockTmuxinator, mockTmux := newConnectorWithMocks(t)
		mockTmuxinator.EXPECT().
			Start("sys").
			Return("", errors.New("boom")).Once()

		connection := model.Connection{
			Found:   true,
			Session: model.SeshSession{Src: "tmuxinator", Name: "sys"},
			New:     true,
		}
		_, err := connectToTmuxinator(c, connection, model.ConnectOpts{})

		assert.ErrorContains(t, err, "failed to start tmuxinator session")
		assert.ErrorContains(t, err, "boom")
		mockTmux.AssertNotCalled(t, "ListSessions")
		mockTmux.AssertNotCalled(t, "SwitchOrAttach", mock.Anything, mock.Anything)
		mockTmux.AssertNotCalled(t, "AttachSession", mock.Anything)
		mockTmux.AssertNotCalled(t, "SwitchClient", mock.Anything)
	})

	for _, switchFlag := range []bool{false, true} {
		t.Run(map[bool]string{false: "attach", true: "switch"}[switchFlag], func(t *testing.T) {
			c, mockTmuxinator, mockTmux := newConnectorWithMocks(t)
			opts := model.ConnectOpts{Switch: switchFlag}
			started := mockTmuxinator.EXPECT().Start("sys").Return("", nil).Once()
			resolved := mockTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{{Name: "sys", ID: "$1"}}, nil).Once()
			resolved.NotBefore(started)
			verb := map[bool]string{false: "attaching", true: "switching"}[switchFlag]
			handoff := mockTmux.EXPECT().SwitchOrAttach("$1", opts).Return(verb+" to tmux session: $1", nil).Once()
			handoff.NotBefore(resolved)

			connection := model.Connection{Session: model.SeshSession{Src: "tmuxinator", Name: "sys"}, New: true}
			msg, err := connectToTmuxinator(c, connection, opts)

			require.NoError(t, err)
			assert.Equal(t, verb+" to tmux session: sys", msg)
		})
	}

	for _, tc := range []struct {
		name     string
		sessions []*model.TmuxSession
		err      error
		want     string
	}{
		{"missing session", nil, nil, `session "sys" not found`},
		{"bridge only", []*model.TmuxSession{{Name: "sys", ID: "$0", SSHBridgeIdentity: "ssh/host/work"}}, nil, `session "sys" not found`},
		{"missing ID", []*model.TmuxSession{{Name: "sys"}}, nil, "has no server-assigned ID"},
		{"inventory failure", nil, errors.New("inventory unavailable"), "inventory unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, mockTmuxinator, mockTmux := newConnectorWithMocks(t)
			started := mockTmuxinator.EXPECT().Start("sys").Return("", nil).Once()
			resolved := mockTmux.EXPECT().ListSessions().Return(tc.sessions, tc.err).Once()
			resolved.NotBefore(started)

			msg, err := connectToTmuxinator(c, model.Connection{Session: model.SeshSession{Name: "sys"}}, model.ConnectOpts{})

			assert.Empty(t, msg)
			assert.ErrorContains(t, err, tc.want)
			mockTmux.AssertNotCalled(t, "SwitchOrAttach", mock.Anything, mock.Anything)
		})
	}

	t.Run("propagates handoff failure", func(t *testing.T) {
		c, mockTmuxinator, mockTmux := newConnectorWithMocks(t)
		mockTmuxinator.EXPECT().Start("sys").Return("", nil).Once()
		mockTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{{Name: "sys", ID: "$1"}}, nil).Once()
		mockTmux.EXPECT().SwitchOrAttach("$1", model.ConnectOpts{}).Return("", assert.AnError).Once()

		_, err := connectToTmuxinator(c, model.Connection{Session: model.SeshSession{Name: "sys"}}, model.ConnectOpts{})

		assert.ErrorIs(t, err, assert.AnError)
	})
}
