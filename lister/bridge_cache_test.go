package lister

import (
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/cache"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestCachingListerNeverStoresBridgeSessions(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{
		{Name: "renamed-bridge", SSHBridgeIdentity: "ssh/host/0"},
		{Name: "sesh-ssh-user", Path: "/user"},
	}, nil).Twice()
	mTmuxinator := tmuxinator.NewMockTmuxinator(t)
	mTmuxinator.EXPECT().List().Return(nil, nil).Twice()
	mZoxide := zoxide.NewMockZoxide(t)
	mZoxide.EXPECT().ListResults().Return(nil, nil).Twice()
	inner := NewLister(model.Config{}, nil, mTmux, mZoxide, mTmuxinator, nil, nil, nil)
	mCache := cache.NewMockCache(t)
	mCache.EXPECT().Read().Return(cache.CachedData{}, errors.New("cold cache")).Once()
	mCache.EXPECT().Write(mock.Anything).Run(func(sessions model.SeshSessions) {
		assert.Equal(t, []string{"tmux:sesh-ssh-user"}, sessions.OrderedIndex)
		assert.Equal(t, model.SeshSessionMap{
			"tmux:sesh-ssh-user": {Src: "tmux", Name: "sesh-ssh-user", Path: "/user"},
		}, sessions.Directory)
	}).Return(nil).Twice()
	cl := NewCachingLister(inner, mCache, false)

	sessions, err := cl.List(ListOptions{Tmux: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"tmux:sesh-ssh-user"}, sessions.OrderedIndex)
	cl.RefreshCache(ListOptions{})
	cl.Wait()
}
