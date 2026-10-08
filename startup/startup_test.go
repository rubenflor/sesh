package startup

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/replacer"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestConfiguredWindowStartupRetainsEachPaneAndSessionCommand(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket"
	config := model.Config{WindowConfigs: []model.WindowConfig{
		{Name: "editor", Path: "~/editor", StartupScript: "script A"},
		{Name: "server", StartupScript: "script B"},
	}}
	session := model.SeshSession{Name: "public name", Path: "/session", WindowNames: []string{"editor", "server"}, TmuxSessionID: "$1", TmuxServerGeneration: generation}
	panes := []model.TmuxPane{
		{SessionID: "$1", WindowID: "@7", PaneID: "%6", ServerGeneration: generation},
		{SessionID: "$1", WindowID: "@8", PaneID: "%7", ServerGeneration: generation},
	}
	tm := tmux.NewMockTmux(t)
	h := home.NewMockHome(t)
	h.EXPECT().ExpandPath("~/editor").Return("/editor", nil).Once()
	h.EXPECT().ExpandPath("/session").Return("/session", nil).Once()
	var previous *mock.Call
	for i, window := range config.WindowConfigs {
		path := []string{"/editor", "/session"}[i]
		create := tm.EXPECT().NewWindowPaneInSession(model.TmuxWindowOpts{Name: window.Name, StartDir: path, TargetSession: "$1"}, generation).Return(panes[i], nil).Once()
		if previous != nil {
			create.NotBefore(previous)
		}
		target := panes[i].SessionID + ":" + panes[i].WindowID + "." + panes[i].PaneID
		previous = tm.EXPECT().SendKeys(target, window.StartupScript, generation).Return("", nil).Once().NotBefore(create)
	}
	next := tm.EXPECT().NextWindowInSession("$1").Return("", nil).Once().NotBefore(previous)
	li := lister.NewMockLister(t)
	li.EXPECT().FindConfigSession(session.Name).Return(model.SeshSession{StartupCommand: "session {}"}, true).Once().NotBefore(next)
	r := replacer.NewMockReplacer(t)
	r.EXPECT().Replace("session {}", map[string]string{"{}": session.Path}).Return("session /session").Once()
	// Intentional session startup keeps its session-only target and ordering.
	tm.EXPECT().SendKeys("$1", "session /session").Return("", nil).Once().NotBefore(next)

	message, err := NewStartup(config, li, tm, h, r).Exec(session, "$1")

	require.NoError(t, err)
	require.Equal(t, "executing startup command: session /session", message)
}

func TestConfiguredWindowStartupFailureStopsDelivery(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket"
	for _, phase := range []string{"creation", "created pane disappears"} {
		t.Run(phase, func(t *testing.T) {
			tm := tmux.NewMockTmux(t)
			h := home.NewMockHome(t)
			h.EXPECT().ExpandPath("/work").Return("/work", nil).Once()
			config := model.Config{WindowConfigs: []model.WindowConfig{{Name: "editor", Path: "/work", StartupScript: "script"}}}
			session := model.SeshSession{Name: "display", WindowNames: []string{"editor"}}
			create := tm.EXPECT().NewWindowPaneInSession(model.TmuxWindowOpts{Name: "editor", StartDir: "/work", TargetSession: "$1"}).Once()
			wantMessage := ""
			if phase == "creation" {
				create.Return(model.TmuxPane{}, assert.AnError)
			} else {
				create.Return(model.TmuxPane{SessionID: "$1", WindowID: "@7", PaneID: "%6", ServerGeneration: generation}, nil)
				wantMessage = "input failed"
				tm.EXPECT().SendKeys("$1:@7.%6", "script", generation).Return(wantMessage, assert.AnError).Once().NotBefore(create)
			}
			// Empty mocks reject fallback input, focus and session startup work.
			message, err := NewStartup(config, lister.NewMockLister(t), tm, h, nil).Exec(session, "$1")
			require.ErrorIs(t, err, assert.AnError)
			require.Equal(t, wantMessage, message)
		})
	}
}

func TestConfiguredWindowWithoutScript(t *testing.T) {
	tm := tmux.NewMockTmux(t)
	h := home.NewMockHome(t)
	h.EXPECT().ExpandPath("/work").Return("/work", nil).Once()
	config := model.Config{WindowConfigs: []model.WindowConfig{{Name: "shell", Path: "/work"}}}
	session := model.SeshSession{Name: "display", Path: "/work", WindowNames: []string{"shell"}}
	create := tm.EXPECT().NewWindowPaneInSession(model.TmuxWindowOpts{Name: "shell", StartDir: "/work", TargetSession: "$1"}).Return(model.TmuxPane{SessionID: "$1", WindowID: "@7", PaneID: "%6", ServerGeneration: "101:1700000000:/tmp/socket"}, nil).Once()
	tm.EXPECT().NextWindowInSession("$1").Return("", nil).Once().NotBefore(create)
	li := lister.NewMockLister(t)
	li.EXPECT().FindConfigSession(session.Name).Return(model.SeshSession{}, false).Once()
	li.EXPECT().FindConfigWildcard(session.Path).Return(model.WildcardConfig{}, false).Once()

	message, err := NewStartup(config, li, tm, h, nil).Exec(session, "$1")

	require.NoError(t, err)
	require.Empty(t, message)
}
