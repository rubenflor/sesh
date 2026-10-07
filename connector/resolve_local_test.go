package connector

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

func TestResolveLocalPane(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mLister := lister.NewMockLister(t)
	session := model.SeshSession{Src: "tmux-pane", Name: "code/1", Path: "/p/repo", TmuxSessionID: "$1", TmuxWindowID: "@3", TmuxPaneID: "%7", TmuxServerGeneration: existingSessionGeneration}
	mTmux.EXPECT().IsAttached().Return(true).Once()
	mLister.EXPECT().ListTmuxPanes().Return(model.SeshSessions{
		OrderedIndex: []string{"tmux-pane:code/%7"},
		Directory: map[string]model.SeshSession{
			"tmux-pane:code/%7": session,
		},
	}, nil).Once()
	c := &RealConnector{tmux: mTmux, lister: mLister}

	connection, err := c.Resolve("code/1")

	require.NoError(t, err)
	require.Equal(t, model.Connection{Found: true, Session: session}, connection)
}

func TestResolveLocalAbsolutePath(t *testing.T) {
	for _, path := range []string{"/p/repo", "/ssh/host/0"} {
		t.Run(path, func(t *testing.T) {
			mTmux := tmux.NewMockTmux(t)
			mLister := lister.NewMockLister(t)
			mHome := home.NewMockHome(t)
			mDir := dir.NewMockDir(t)
			mNamer := namer.NewMockNamer(t)
			mTmux.EXPECT().IsAttached().Return(true).Once()
			mLister.EXPECT().FindTmuxSession(path).Return(model.SeshSession{}, false).Once()
			mLister.EXPECT().FindTmuxinatorConfig(path).Return(model.SeshSession{}, false).Once()
			mLister.EXPECT().FindConfigSession(path).Return(model.SeshSession{}, false).Once()
			mHome.EXPECT().ExpandPath(path).Return(path, nil).Twice()
			mDir.EXPECT().Dir(path).Return(true, path).Twice()
			mLister.EXPECT().FindConfigWildcard(path).Return(model.WildcardConfig{}, false).Once()
			mNamer.EXPECT().Name(path).Return("repo", nil).Once()
			mLister.EXPECT().FindTmuxSessionByBase("repo").Return(model.SeshSession{}, false).Once()
			c := &RealConnector{tmux: mTmux, lister: mLister, home: mHome, dir: mDir, namer: mNamer}

			connection, err := c.Resolve(path)

			require.NoError(t, err)
			require.Equal(t, model.Connection{
				Found: true, New: true, AddToZoxide: true,
				Session: model.SeshSession{Src: "dir", Name: "repo", Path: path},
			}, connection)
		})
	}
}

func TestResolveLocalAlias(t *testing.T) {
	mTmux := tmux.NewMockTmux(t)
	mLister := lister.NewMockLister(t)
	session := model.SeshSession{Src: "tmux", Name: "wallpaper", Path: "/p/wallpaper"}
	mTmux.EXPECT().IsAttached().Return(false).Once()
	mLister.EXPECT().FindTmuxSession("wallpaper").Return(session, true).Once()
	c := &RealConnector{
		config: model.Config{SessionConfigs: []model.SessionConfig{{Name: "wallpaper", Alias: "wp"}}},
		tmux:   mTmux,
		lister: mLister,
	}

	connection, err := c.Resolve("WP")

	require.NoError(t, err)
	require.Equal(t, model.Connection{Found: true, AddToZoxide: true, Session: session}, connection)
}
