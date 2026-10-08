package connector

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/startup"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// windowConnector wires the mocks every window connect test needs, with the
// target session already attached so the tests only have to describe the
// window behaviour they care about.
func windowConnector(t *testing.T) (*RealConnector, *tmux.MockTmux, *lister.MockLister, *dir.MockDir, *home.MockHome) {
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return([]*model.TmuxSession{{Name: "second-brain", ID: "$9", ServerGeneration: existingSessionGeneration, Path: "/home/dev/brain"}}, nil).Maybe()
	mLister := lister.NewMockLister(t)
	mDir := dir.NewMockDir(t)
	mHome := home.NewMockHome(t)

	c := NewConnector(
		model.Config{}, mDir, mHome, mLister, nil, nil, mTmux, nil, nil, nil,
	).(*RealConnector)

	return c, mTmux, mLister, mDir, mHome
}

func attachedTo(mLister *lister.MockLister, name string, path string) {
	mLister.EXPECT().GetAttachedTmuxSession().Return(model.SeshSession{Name: name, Path: path, TmuxSessionID: "$9", TmuxServerGeneration: existingSessionGeneration}, true)
}

// notADirectory keeps a plain window name from being read as a path.
func notADirectory(mHome *home.MockHome, mDir *dir.MockDir, name string) {
	mHome.EXPECT().ExpandPath(name).Return(name, nil)
	mDir.EXPECT().Dir(name).Return(false, "")
}

func TestConnectWindow(t *testing.T) {
	t.Run("sends the command into a window that already exists", func(t *testing.T) {
		c, mTmux, mLister, mDir, mHome := windowConnector(t)
		attachedTo(mLister, "second-brain", "/home/dev/brain")
		notADirectory(mHome, mDir, "notes")
		mTmux.EXPECT().ListWindows("$9", existingSessionGeneration).Return([]*model.TmuxWindow{
			{ID: "@1", ServerGeneration: existingSessionGeneration, Name: "shell", Index: 1},
			{ID: "@7", ServerGeneration: existingSessionGeneration, Name: "notes", Index: 4},
		}, nil)
		mTmux.EXPECT().SendKeys("$9:@7", "claude", existingSessionGeneration).Return("", nil)
		mTmux.EXPECT().SelectWindow("$9:@7", existingSessionGeneration).Return("", nil)
		mTmux.EXPECT().SwitchOrAttach("$9:@7", model.ConnectOpts{}, existingSessionGeneration).Return("", nil)

		target, err := c.ConnectWindow(model.WindowConnectOpts{Name: "notes", Command: "claude"})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:4", target)
	})

	t.Run("creates the window when the name doesn't match", func(t *testing.T) {
		c, mTmux, mLister, mDir, mHome := windowConnector(t)
		attachedTo(mLister, "second-brain", "/home/dev/brain")
		notADirectory(mHome, mDir, "notes")
		mTmux.EXPECT().ListWindows("$9", existingSessionGeneration).Return([]*model.TmuxWindow{{Name: "shell", Index: 1}}, nil)
		mTmux.EXPECT().NewWindowInSession(model.TmuxWindowOpts{
			Name:          "notes",
			StartDir:      "/home/dev/brain",
			TargetSession: "$9",
			Command:       "claude",
		}, existingSessionGeneration).Return("second-brain:2", nil)
		mTmux.EXPECT().SwitchOrAttach("$9", model.ConnectOpts{}, existingSessionGeneration).Return("", nil)

		target, err := c.ConnectWindow(model.WindowConnectOpts{Name: "notes", Command: "claude"})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:2", target)
	})

	t.Run("always creates when no name is given", func(t *testing.T) {
		c, mTmux, mLister, _, _ := windowConnector(t)
		attachedTo(mLister, "second-brain", "/home/dev/brain")
		mTmux.EXPECT().NewWindowInSession(model.TmuxWindowOpts{
			StartDir:      "/home/dev/brain",
			TargetSession: "$9",
		}, existingSessionGeneration).Return("second-brain:2", nil)
		mTmux.EXPECT().SwitchOrAttach("$9", model.ConnectOpts{}, existingSessionGeneration).Return("", nil)

		target, err := c.ConnectWindow(model.WindowConnectOpts{})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:2", target)
	})

	t.Run("--new creates even when the name matches, without listing windows", func(t *testing.T) {
		c, mTmux, mLister, mDir, mHome := windowConnector(t)
		attachedTo(mLister, "second-brain", "/home/dev/brain")
		notADirectory(mHome, mDir, "claude")
		mTmux.EXPECT().NewWindowInSession(model.TmuxWindowOpts{
			Name:          "claude",
			StartDir:      "/home/dev/brain",
			TargetSession: "$9",
			Command:       "claude",
			Background:    true,
		}, existingSessionGeneration).Return("second-brain:5", nil)

		target, err := c.ConnectWindow(model.WindowConnectOpts{
			Name: "claude", Command: "claude", New: true, Background: true,
		})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:5", target)
	})

	t.Run("reuse picks the lowest-indexed window when names are shared", func(t *testing.T) {
		c, mTmux, mLister, mDir, mHome := windowConnector(t)
		attachedTo(mLister, "second-brain", "/home/dev/brain")
		notADirectory(mHome, mDir, "claude")
		mTmux.EXPECT().ListWindows("$9", existingSessionGeneration).Return([]*model.TmuxWindow{
			{ID: "@12", ServerGeneration: existingSessionGeneration, Name: "claude", Index: 2},
			{ID: "@5", ServerGeneration: existingSessionGeneration, Name: "claude", Index: 5},
		}, nil)
		mTmux.EXPECT().SendKeys("$9:@12", "another prompt", existingSessionGeneration).Return("", nil)

		target, err := c.ConnectWindow(model.WindowConnectOpts{
			Name: "claude", Command: "another prompt", Background: true,
		})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:2", target)
	})

	t.Run("background leaves the client where it is", func(t *testing.T) {
		c, mTmux, mLister, mDir, mHome := windowConnector(t)
		attachedTo(mLister, "second-brain", "/home/dev/brain")
		notADirectory(mHome, mDir, "notes")
		mTmux.EXPECT().ListWindows("$9", existingSessionGeneration).Return(nil, nil)
		mTmux.EXPECT().NewWindowInSession(model.TmuxWindowOpts{
			Name:          "notes",
			StartDir:      "/home/dev/brain",
			TargetSession: "$9",
			Background:    true,
		}, existingSessionGeneration).Return("second-brain:2", nil)

		target, err := c.ConnectWindow(model.WindowConnectOpts{Name: "notes", Background: true})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:2", target)
	})

	t.Run("names a directory argument after its basename and roots it there", func(t *testing.T) {
		c, mTmux, mLister, mDir, mHome := windowConnector(t)
		attachedTo(mLister, "second-brain", "/home/dev/brain")
		mHome.EXPECT().ExpandPath("~/c/sesh").Return("/home/dev/c/sesh", nil)
		mDir.EXPECT().Dir("/home/dev/c/sesh").Return(true, "/home/dev/c/sesh")
		mTmux.EXPECT().ListWindows("$9", existingSessionGeneration).Return(nil, nil)
		mTmux.EXPECT().NewWindowInSession(model.TmuxWindowOpts{
			Name:          "sesh",
			StartDir:      "/home/dev/c/sesh",
			TargetSession: "$9",
		}, existingSessionGeneration).Return("second-brain:2", nil)
		mTmux.EXPECT().SwitchOrAttach("$9", model.ConnectOpts{}, existingSessionGeneration).Return("", nil)

		target, err := c.ConnectWindow(model.WindowConnectOpts{Name: "~/c/sesh"})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:2", target)
	})

	t.Run("reuses the window a directory argument created on an earlier run", func(t *testing.T) {
		c, mTmux, mLister, mDir, mHome := windowConnector(t)
		attachedTo(mLister, "second-brain", "/home/dev/brain")
		mHome.EXPECT().ExpandPath("~/c/sesh").Return("/home/dev/c/sesh", nil)
		mDir.EXPECT().Dir("/home/dev/c/sesh").Return(true, "/home/dev/c/sesh")
		mTmux.EXPECT().ListWindows("$9", existingSessionGeneration).Return([]*model.TmuxWindow{{ID: "@73", ServerGeneration: existingSessionGeneration, Name: "sesh", Index: 3}}, nil)
		mTmux.EXPECT().SelectWindow("$9:@73", existingSessionGeneration).Return("", nil)
		mTmux.EXPECT().SwitchOrAttach("$9:@73", model.ConnectOpts{}, existingSessionGeneration).Return("", nil)

		target, err := c.ConnectWindow(model.WindowConnectOpts{Name: "~/c/sesh"})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:3", target)
	})

	t.Run("switches the client explicitly when triggered from outside tmux", func(t *testing.T) {
		c, mTmux, mLister, _, _ := windowConnector(t)
		mLister.EXPECT().FindTmuxSession("second-brain").Return(model.SeshSession{
			Src: "tmux", Name: "second-brain", Path: "/home/dev/brain", TmuxSessionID: "$9", TmuxServerGeneration: existingSessionGeneration,
		}, true)
		mTmux.EXPECT().IsAttached().Return(false)
		mTmux.EXPECT().NewWindowInSession(model.TmuxWindowOpts{
			StartDir:      "/home/dev/brain",
			TargetSession: "$9",
			Command:       "claude",
		}, existingSessionGeneration).Return("second-brain:2", nil)
		mTmux.EXPECT().ListClients().Return([]model.TmuxClient{
			{Name: "/dev/ttys002", TTY: "/dev/ttys002", SessionID: "$2"},
		}, nil)
		mTmux.EXPECT().ResolveClient().Return("/dev/ttys002")
		mTmux.EXPECT().SwitchClientTarget("/dev/ttys002", "$9", existingSessionGeneration).Return("", nil)

		mZoxide := zoxide.NewMockZoxide(t)
		mZoxide.EXPECT().Add("/home/dev/brain").Return(nil)
		c.zoxide = mZoxide
		mFocuser := focuser.NewMockFocuser(t)
		mFocuser.EXPECT().Activate("").Return(false, nil)
		c.focuser = mFocuser

		target, err := c.ConnectWindow(model.WindowConnectOpts{
			Session: "second-brain", Command: "claude", Switch: true,
		})

		require.NoError(t, err)
		assert.Equal(t, "second-brain:2", target)
	})

	t.Run("errors when no session is given and tmux isn't attached", func(t *testing.T) {
		c, _, mLister, _, _ := windowConnector(t)
		mLister.EXPECT().GetAttachedTmuxSession().Return(model.SeshSession{}, false)

		_, err := c.ConnectWindow(model.WindowConnectOpts{Name: "notes"})

		assert.ErrorContains(t, err, "use --target")
	})
}

func TestExistingWindowReuseHandoffKeepsWindowID(t *testing.T) {
	for _, mode := range []string{"attach", "switch", "detached client", "detached fallback", "no clients"} {
		t.Run(mode, func(t *testing.T) {
			c, m, li, _, _ := windowConnector(t)
			attachedTo(li, "second-brain", "/home/dev/brain")
			m.EXPECT().ListWindows("$9", existingSessionGeneration).Return([]*model.TmuxWindow{{ID: "@7", ServerGeneration: existingSessionGeneration, Name: "notes", Index: 4}}, nil).Once()
			selection := m.EXPECT().SelectWindow("$9:@7", existingSessionGeneration).Return("", nil).Once()
			doSwitch := mode != "attach"
			if mode == "attach" || mode == "switch" {
				if doSwitch {
					m.EXPECT().IsAttached().Return(true).Once()
				}
				m.EXPECT().SwitchOrAttach("$9:@7", model.ConnectOpts{Switch: doSwitch}, existingSessionGeneration).Return("", nil).Once().NotBefore(selection)
			} else {
				m.EXPECT().IsAttached().Return(false).Once()
				clients := []model.TmuxClient{{Name: "client"}}
				if mode == "no clients" {
					clients = nil
					m.EXPECT().SelectWindow("$9:@7", existingSessionGeneration).Return("", nil).Once().NotBefore(selection)
				} else {
					client := "client"
					if mode == "detached fallback" {
						client = ""
						m.EXPECT().SwitchClient("$9:@7", existingSessionGeneration).Return("", nil).Once()
					} else {
						m.EXPECT().SwitchClientTarget(client, "$9:@7", existingSessionGeneration).Return("", nil).Once()
					}
					m.EXPECT().ResolveClient().Return(client).Once()
				}
				m.EXPECT().ListClients().Return(clients, nil).Once().NotBefore(selection)
				f := focuser.NewMockFocuser(t)
				f.EXPECT().Activate("").Return(true, nil).Once()
				c.focuser = f
			}

			target, err := c.ConnectWindow(model.WindowConnectOpts{Name: "notes", Path: "/home/dev/brain", Switch: doSwitch})

			require.NoError(t, err)
			require.Equal(t, "second-brain:4", target)
		})
	}
}

func TestExistingWindowReuseFailsClosed(t *testing.T) {
	for _, phase := range []string{"missing ID", "display name instead of ID", "input", "selection", "handoff", "background verification", "no-client verification"} {
		t.Run(phase, func(t *testing.T) {
			c, m, li, _, _ := windowConnector(t)
			attachedTo(li, "second-brain", "/home/dev/brain")
			window := &model.TmuxWindow{ID: "@7", ServerGeneration: existingSessionGeneration, Name: "notes", Index: 4}
			switch phase {
			case "missing ID":
				window.ID = ""
			case "display name instead of ID":
				window.ID = "notes"
			}
			m.EXPECT().ListWindows("$9", existingSessionGeneration).Return([]*model.TmuxWindow{window}, nil).Once()
			opts := model.WindowConnectOpts{Name: "notes", Path: "/home/dev/brain", Command: "command"}
			if phase == "background verification" {
				opts.Command, opts.Background = "", true
				m.EXPECT().CapturePane("$9:@7", existingSessionGeneration).Return("", assert.AnError).Once()
			} else if phase != "missing ID" && phase != "display name instead of ID" {
				var inputErr, selectionErr error
				if phase == "input" {
					inputErr = assert.AnError
				} else if phase == "selection" {
					selectionErr = assert.AnError
				}
				input := m.EXPECT().SendKeys("$9:@7", opts.Command, existingSessionGeneration).Return("", inputErr).Once()
				if inputErr == nil {
					selection := m.EXPECT().SelectWindow("$9:@7", existingSessionGeneration).Return("", selectionErr).Once().NotBefore(input)
					if phase == "handoff" {
						m.EXPECT().SwitchOrAttach("$9:@7", model.ConnectOpts{}, existingSessionGeneration).Return("", assert.AnError).Once().NotBefore(selection)
					} else if phase == "no-client verification" {
						opts.Switch = true
						m.EXPECT().IsAttached().Return(false).Once()
						m.EXPECT().ListClients().Return(nil, nil).Once()
						m.EXPECT().SelectWindow("$9:@7", existingSessionGeneration).Return("", assert.AnError).Once().NotBefore(selection)
						c.focuser = focuser.NewMockFocuser(t) // No terminal activation after failed tuple verification.
					}
				}
			}

			target, err := c.ConnectWindow(opts)

			require.Error(t, err)
			require.Empty(t, target)
		})
	}
}

func TestExistingWindowReuseFromNewSessionKeepsInventoryGeneration(t *testing.T) {
	m := tmux.NewMockTmux(t)
	li := lister.NewMockLister(t)
	zo := zoxide.NewMockZoxide(t)
	st := startup.NewMockStartup(t)
	session := model.SeshSession{Src: "config", Name: "second-brain", Path: "/home/dev/brain"}
	m.EXPECT().IsAttached().Return(false).Once()
	li.EXPECT().FindTmuxSession(session.Name).Return(model.SeshSession{}, false).Once()
	li.EXPECT().FindTmuxinatorConfig(session.Name).Return(model.SeshSession{}, false).Once()
	li.EXPECT().FindConfigSession(session.Name).Return(session, true).Once()
	beforeCreation := m.EXPECT().ListSessions().Return(nil, nil).Once()
	zo.EXPECT().Add(session.Path).Return(nil).Once()
	created := m.EXPECT().NewSession(session.Name, session.Path).Return("", nil).Once().NotBefore(beforeCreation)
	m.EXPECT().ListSessions().Return([]*model.TmuxSession{{ID: "$9", Name: session.Name}}, nil).Once().NotBefore(created)
	st.EXPECT().Exec(session, "$9").Return("", nil).Once()
	m.EXPECT().ListWindows("$9").Return([]*model.TmuxWindow{{ID: "@7", ServerGeneration: existingSessionGeneration, Name: "notes", Index: 4}}, nil).Once()
	m.EXPECT().SendKeys("$9:@7", "command", existingSessionGeneration).Return("", nil).Once()
	c := &RealConnector{tmux: m, lister: li, zoxide: zo, startup: st}

	target, err := c.ConnectWindow(model.WindowConnectOpts{Session: session.Name, Name: "notes", Path: session.Path, Command: "command", Background: true})

	require.NoError(t, err)
	require.Equal(t, "second-brain:4", target)
}

func TestExistingWindowReuseRejectsInventoryGeneration(t *testing.T) {
	for _, generation := range []string{"", "unknown", "0::@7::" + existingSessionGeneration, "102:1700000001:/tmp/tmux-fixture"} {
		for _, background := range []bool{false, true} {
			t.Run(generation+"/"+map[bool]string{false: "input and focus", true: "background verification"}[background], func(t *testing.T) {
				c, m, li, _, _ := windowConnector(t)
				attachedTo(li, "second-brain", "/home/dev/brain")
				m.EXPECT().ListWindows("$9", existingSessionGeneration).Return([]*model.TmuxWindow{{ID: "@7", ServerGeneration: generation, Name: "notes", Index: 4}}, nil).Once()
				command := "must not reach any window"
				if background {
					command = ""
				}

				target, err := c.ConnectWindow(model.WindowConnectOpts{Name: "notes", Path: "/home/dev/brain", Command: command, Background: background})

				require.ErrorContains(t, err, "server generation")
				require.Empty(t, target)
				// Strict mocks reject any input, selection, focus, capture, or creation.
			})
		}
	}
}
