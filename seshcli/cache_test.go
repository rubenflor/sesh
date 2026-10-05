package seshcli

import (
	"path/filepath"
	"testing"

	"github.com/joshmedeski/sesh/v2/cache"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheRefreshStoresLocalSourcesWithSSHConfigured(t *testing.T) {
	config := model.Config{
		Cache:          true,
		SSH:            model.SSHConfig{Hosts: []string{"server"}},
		SessionConfigs: []model.SessionConfig{{Name: "configured", Path: "/configured"}},
	}
	runner := execwrap.NewMockExec(t)
	localTmux := tmux.NewMockTmux(t)
	localTmux.On("ListSessions").Return([]*model.TmuxSession{{Name: "local", Path: "/local"}}, nil).Once()
	localTmuxinator := tmuxinator.NewMockTmuxinator(t)
	localTmuxinator.On("List").Return([]*model.TmuxinatorConfig{{Name: "project"}}, nil).Once()
	localZoxide := zoxide.NewMockZoxide(t)
	localZoxide.On("ListResults").Return([]*model.ZoxideResult{{Path: "/frequent", Score: 10}}, nil).Once()
	localHome := home.NewMockHome(t)
	localHome.On("ExpandPath", "/configured").Return("/configured", nil).Once()
	localHome.On("ShortenHome", "/frequent").Return("frequent", nil).Once()
	inner := lister.NewLister(config, localHome, localTmux, localZoxide, localTmuxinator, nil, nil, runner)
	fileCache := cache.NewFileCacheWithPath(filepath.Join(t.TempDir(), "sessions.gob"))
	cachedLister := lister.NewCachingLister(inner, fileCache, true)
	cmd := newCacheRefreshCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) {
		return &Deps{Config: config, Lister: cachedLister, CachingLister: cachedLister}, nil
	})

	require.NoError(t, cmd.Execute())
	cached, err := fileCache.Read()
	require.NoError(t, err, "refresh must finish writing before the command returns")
	assert.Equal(t, []string{"tmux:local", "config:configured", "tmuxinator:project", "zoxide:frequent"}, cached.Sessions.OrderedIndex)
	require.Len(t, cached.Sessions.Directory, 4)
	for _, session := range cached.Sessions.Directory {
		assert.NotEqual(t, "ssh", session.Src)
	}
	runner.AssertNumberOfCalls(t, "RunContext", 0)
}

func TestCacheRefreshDisabledDoesNotListSources(t *testing.T) {
	inner := lister.NewMockLister(t)
	runner := execwrap.NewMockExec(t)
	cmd := newCacheRefreshCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) {
		return &Deps{
			BaseDeps: BaseDeps{Exec: runner},
			Config:   model.Config{SSH: model.SSHConfig{Hosts: []string{"server"}}},
			Lister:   inner,
		}, nil
	})

	require.NoError(t, cmd.Execute())
	inner.AssertNumberOfCalls(t, "List", 0)
	runner.AssertNumberOfCalls(t, "RunContext", 0)
}
