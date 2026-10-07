package connector

import (
	"errors"
	"fmt"
	"testing"

	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSHBridgeCreatesMarkedSession(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return(nil, nil).Once()
	mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
		Name:           "sesh-ssh-71733fb26ad99c67e0bf6bbe-0",
		Command:        `exec ssh -tt -- 'host' 'tmux attach-session -t '\''=0'\'''`,
		Environment:    []string{"SESH_SSH_IDENTITY=ssh/host/0"},
		SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
		PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
	}).Return("$8 %9", nil).Once()
	c := &RealConnector{tmux: mTmux}

	name, err := c.ensureSSHBridge("ssh/host/0")

	require.NoError(t, err)
	assert.Equal(t, "$8", name)
}

func TestSSHBridgeReusesExactMarkerRegardlessOfName(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{
		{Name: "sesh-ssh-71733fb26ad99c67e0bf6bbe-0", ID: "$1"},
		{Name: "renamed bridge", ID: "$8"},
	}, nil).Once()
	mTmux.EXPECT().GetSessionEnvironment("$1", "SESH_SSH_IDENTITY").Return("", nil).Once()
	mTmux.EXPECT().GetSessionEnvironment("$8", "SESH_SSH_IDENTITY").Return("ssh/host/0", nil).Once()
	mTmux.EXPECT().IsPaneDead("$8").Return(false, nil).Once()
	c := &RealConnector{tmux: mTmux}

	name, err := c.ensureSSHBridge("ssh/host/0")

	require.NoError(t, err)
	assert.Equal(t, "$8", name)
}

func TestSSHBridgeDoesNotReuseMarkerPrefix(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{
		{Name: "sesh-ssh-71733fb26ad99c67e0bf6bbe-0", ID: "$1"},
	}, nil).Once()
	mTmux.EXPECT().GetSessionEnvironment("$1", "SESH_SSH_IDENTITY").
		Return("ssh/host/0\nextra", nil).Once()
	mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
		Name:           "sesh-ssh-71733fb26ad99c67e0bf6bbe-1",
		Command:        `exec ssh -tt -- 'host' 'tmux attach-session -t '\''=0'\'''`,
		Environment:    []string{"SESH_SSH_IDENTITY=ssh/host/0"},
		SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
		PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
	}).Return("$8 %9", nil).Once()
	c := &RealConnector{tmux: mTmux}

	name, err := c.ensureSSHBridge("ssh/host/0")

	require.NoError(t, err)
	assert.Equal(t, "$8", name)
}

func TestSSHBridgeSkipsCollisionsAndLeavesSessionsVisible(t *testing.T) {
	const base = "sesh-ssh-71733fb26ad99c67e0bf6bbe"
	sessions := []*model.TmuxSession{
		{Name: base + "-0", ID: "$4", Path: "/user"},
		{Name: base + "-1", ID: "$9", Path: "/other"},
	}
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return(sessions, nil).Times(3)
	mTmux.EXPECT().GetSessionEnvironment("$4", "SESH_SSH_IDENTITY").Return("", nil).Once()
	mTmux.EXPECT().GetSessionEnvironment("$9", "SESH_SSH_IDENTITY").Return("ssh/other/0", nil).Once()
	mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
		Name:           base + "-2",
		Command:        `exec ssh -tt -- 'host' 'tmux attach-session -t '\''=0'\'''`,
		Environment:    []string{"SESH_SSH_IDENTITY=ssh/host/0"},
		SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
		PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
	}).Return("$8 %9", nil).Once()
	c := &RealConnector{tmux: mTmux}

	name, err := c.ensureSSHBridge("ssh/host/0")

	require.NoError(t, err)
	assert.Equal(t, "$8", name)
	l := lister.NewLister(model.Config{}, nil, mTmux, nil, nil, nil, nil, nil)
	for _, session := range sessions {
		visible, found := l.FindTmuxSession(session.Name)
		require.True(t, found)
		assert.Equal(t, session.Name, visible.Name)
		assert.Equal(t, session.Path, visible.Path)
	}
}

func TestSSHBridgeBoundedCollisionSlots(t *testing.T) {
	const base = "sesh-ssh-71733fb26ad99c67e0bf6bbe"
	for _, occupied := range []int{7, 8} {
		t.Run(fmt.Sprintf("%d occupied", occupied), func(t *testing.T) {
			mTmux := tmux.NewMockTmux(t)
			sessions := make([]*model.TmuxSession, 0, occupied)
			for slot := 0; slot < occupied; slot++ {
				id := fmt.Sprintf("$%d", slot+1)
				sessions = append(sessions, &model.TmuxSession{Name: fmt.Sprintf("%s-%d", base, slot), ID: id})
				mTmux.EXPECT().GetSessionEnvironment(id, "SESH_SSH_IDENTITY").Return("ssh/other/0", nil).Once()
			}
			mTmux.EXPECT().ListSessions().Return(sessions, nil).Once()
			if occupied == 7 {
				mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
					Name:           base + "-7",
					Command:        `exec ssh -tt -- 'host' 'tmux attach-session -t '\''=0'\'''`,
					Environment:    []string{"SESH_SSH_IDENTITY=ssh/host/0"},
					SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
					PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
				}).Return("$8 %9", nil).Once()
			}
			c := &RealConnector{tmux: mTmux}

			name, err := c.ensureSSHBridge("ssh/host/0")

			if occupied == 8 {
				require.ErrorContains(t, err, "SSH bridge name collision")
				assert.ErrorContains(t, err, "all 8 slots are occupied")
				assert.Empty(t, name)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "$8", name)
		})
	}
}

func TestSSHBridgeNamesUseCanonicalIdentity(t *testing.T) {
	for _, tt := range []struct {
		identity, wantName string
	}{
		{"ssh/other/0", "sesh-ssh-88f53904e3aca8f7468c0289-0"},
		{"ssh/my%2Fhost/work%252F%20caf%C3%A9", "sesh-ssh-3d3ae1f1b137cf5107f2606c-0"},
	} {
		t.Run(tt.identity, func(t *testing.T) {
			command, err := sshAttachCommand(tt.identity)
			require.NoError(t, err)
			mTmux := tmux.NewMockTmux(t)
			mTmux.EXPECT().ListSessions().Return(nil, nil).Once()
			mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
				Name: tt.wantName, Command: command,
				Environment:    []string{"SESH_SSH_IDENTITY=" + tt.identity},
				SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
				PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
			}).Return("$8 %9", nil).Once()
			c := &RealConnector{tmux: mTmux}

			name, err := c.ensureSSHBridge(tt.identity)

			require.NoError(t, err)
			assert.Equal(t, "$8", name)
		})
	}
}

func TestSSHBridgePropagatesTmuxFailures(t *testing.T) {
	wantErr := errors.New("tmux failure")
	for _, stage := range []string{"list", "marker", "pane", "kill", "create"} {
		t.Run(stage, func(t *testing.T) {
			mTmux := tmux.NewMockTmux(t)
			switch stage {
			case "list":
				mTmux.EXPECT().ListSessions().Return(nil, wantErr).Once()
			case "marker":
				mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{{Name: "user", ID: "$1"}}, nil).Once()
				mTmux.EXPECT().GetSessionEnvironment("$1", "SESH_SSH_IDENTITY").Return("", wantErr).Once()
			case "pane", "kill":
				mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{{Name: "bridge", ID: "$8", ServerGeneration: "101:1700000000:/tmp/bridge"}}, nil).Once()
				mTmux.EXPECT().GetSessionEnvironment("$8", model.SSHBridgeIdentityEnv).Return("ssh/host/0", nil).Once()
				if stage == "pane" {
					mTmux.EXPECT().IsPaneDead("$8").Return(false, wantErr).Once()
				} else {
					mTmux.EXPECT().IsPaneDead("$8").Return(true, nil).Once()
					mTmux.EXPECT().KillSessionInGeneration("$8", "101:1700000000:/tmp/bridge").Return("", wantErr).Once()
				}
			case "create":
				mTmux.EXPECT().ListSessions().Return(nil, nil).Once()
				mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
					Name:           "sesh-ssh-71733fb26ad99c67e0bf6bbe-0",
					Command:        `exec ssh -tt -- 'host' 'tmux attach-session -t '\''=0'\'''`,
					Environment:    []string{"SESH_SSH_IDENTITY=ssh/host/0"},
					SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
					PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
				}).Return("", wantErr).Once()
			}
			c := &RealConnector{tmux: mTmux}

			name, err := c.ensureSSHBridge("ssh/host/0")

			require.ErrorIs(t, err, wantErr)
			assert.Empty(t, name)
		})
	}
}

func TestSSHBridgeRejectsInvalidIdentityBeforeLookup(t *testing.T) {
	c := &RealConnector{tmux: tmux.NewMockTmux(t)}

	name, err := c.ensureSSHBridge("ssh/host/work%2f1")

	require.ErrorContains(t, err, "non-canonical encoding")
	assert.Empty(t, name)
}

func TestSSHBridgeReplacesDeadExactMarker(t *testing.T) {
	const generation = "101:1700000000:/tmp/bridge"
	m := tmux.NewMockTmux(t)
	m.EXPECT().ListSessions().Return([]*model.TmuxSession{
		{Name: "sesh-ssh-71733fb26ad99c67e0bf6bbe-0", ID: "$1"},
		{Name: "renamed dead bridge", ID: "$8", ServerGeneration: generation},
	}, nil).Once()
	m.EXPECT().GetSessionEnvironment("$1", model.SSHBridgeIdentityEnv).Return("", nil).Once()
	marker := m.EXPECT().GetSessionEnvironment("$8", model.SSHBridgeIdentityEnv).Return("ssh/host/0", nil).Once()
	dead := m.EXPECT().IsPaneDead("$8").Return(true, nil).Once().NotBefore(marker)
	kill := m.EXPECT().KillSessionInGeneration("$8", generation).Return("", nil).Once().NotBefore(dead)
	m.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
		Name:           "sesh-ssh-71733fb26ad99c67e0bf6bbe-1",
		Command:        `exec ssh -tt -- 'host' 'tmux attach-session -t '\''=0'\'''`,
		Environment:    []string{"SESH_SSH_IDENTITY=ssh/host/0"},
		SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
		PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
	}).Return("$8 %9", nil).Once().NotBefore(kill)
	c := &RealConnector{tmux: m}
	name, err := c.ensureSSHBridge("ssh/host/0")
	require.NoError(t, err)
	assert.Equal(t, "$8", name)
}

func TestSSHBridgeCleanupFailsClosed(t *testing.T) {
	for _, generation := range []string{"", "invalid", "101:1700000000:/tmp/old-server"} {
		t.Run(generation, func(t *testing.T) {
			wantErr := errors.New("unverified or stale tmux server generation")
			m := tmux.NewMockTmux(t)
			m.EXPECT().ListSessions().Return([]*model.TmuxSession{
				{Name: "renamed dead bridge", ID: "$1", ServerGeneration: generation},
			}, nil).Once()
			marker := m.EXPECT().GetSessionEnvironment("$1", model.SSHBridgeIdentityEnv).
				Return("ssh/host/0", nil).Once()
			dead := m.EXPECT().IsPaneDead("$1").Return(true, nil).Once().NotBefore(marker)
			m.EXPECT().KillSessionInGeneration("$1", generation).
				Return("", wantErr).Once().NotBefore(dead)
			c := &RealConnector{tmux: m}

			id, err := c.ensureSSHBridge("ssh/host/0")

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to remove stale SSH bridge")
			require.Empty(t, id)
			m.AssertNumberOfCalls(t, "KillSession", 0)
			m.AssertNumberOfCalls(t, "NewSessionWithCommand", 0)
		})
	}
}
