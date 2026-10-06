package lister

import (
	"encoding/json"
	"fmt"
	"log"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTmuxDiscoveryFiltersBridgeMarkersBeforeConversion(t *testing.T) {
	m := tmux.NewMockTmux(t)
	m.EXPECT().ListSessions().Return([]*model.TmuxSession{
		{ID: "$1", ServerGeneration: "101:1700000000:/tmp/tmux-fixture", Name: "current", Path: "/current"},
		{ID: "$0", Name: "renamed-bridge", Path: "/bridge", SSHBridgeIdentity: "ssh/host/0"},
		{ID: "$2", ServerGeneration: "101:1700000000:/tmp/tmux-fixture", Name: "sesh-ssh-user", Path: "/user"},
	}, nil)
	l := NewLister(model.Config{}, nil, m, nil, nil, nil, nil, nil)

	sessions, err := l.List(ListOptions{Tmux: true})

	require.NoError(t, err)
	assert.Equal(t, []string{"tmux:current", "tmux:sesh-ssh-user"}, sessions.OrderedIndex)
	assert.Len(t, sessions.Directory, 2)
	assert.Equal(t, "$1", sessions.Directory["tmux:current"].TmuxSessionID)
	assert.Equal(t, "$2", sessions.Directory["tmux:sesh-ssh-user"].TmuxSessionID)
	assert.Equal(t, "101:1700000000:/tmp/tmux-fixture", sessions.Directory["tmux:current"].TmuxServerGeneration)
	assert.Equal(t, "101:1700000000:/tmp/tmux-fixture", sessions.Directory["tmux:sesh-ssh-user"].TmuxServerGeneration)
	public, err := json.Marshal(sessions.Directory)
	require.NoError(t, err)
	assert.NotContains(t, string(public), "TmuxSessionID")
	assert.NotContains(t, string(public), "TmuxServerGeneration")
	_, found := l.FindTmuxSession("renamed-bridge")
	assert.False(t, found)
	_, found = l.FindTmuxSessionByBase("renamed-bridge")
	assert.False(t, found)
	user, found := l.FindTmuxSession("sesh-ssh-user")
	require.True(t, found)
	assert.Equal(t, "/user", user.Path)
	last, found := l.GetLastTmuxSession()
	require.True(t, found)
	assert.Equal(t, "sesh-ssh-user", last.Name)
}

func TestGetAttachedTmuxSessionBridgeUnavailable(t *testing.T) {
	for _, marker := range []string{"ssh/host/work", ""} {
		t.Run("marker="+marker, func(t *testing.T) {
			m := tmux.NewMockTmux(t)
			m.EXPECT().IsAttached().Return(true)
			m.EXPECT().GetCurrentSession().Return("sesh-ssh-user", nil)
			m.EXPECT().ListSessions().Return([]*model.TmuxSession{
				{Name: "detached", Path: "/detached"},
				{Name: "sesh-ssh-user", Path: "/user", Attached: 1, SSHBridgeIdentity: marker},
				{Name: "other-client", Path: "/other", Attached: 1},
			}, nil)
			l := NewLister(model.Config{}, nil, m, nil, nil, nil, nil, nil)

			session, found := l.GetAttachedTmuxSession()

			if marker != "" {
				assert.False(t, found)
				assert.Equal(t, model.SeshSession{}, session)
			} else {
				require.True(t, found)
				assert.Equal(t, "sesh-ssh-user", session.Name)
				assert.Equal(t, "/user", session.Path)
			}
		})
	}
}

func TestAttachedSessionUsesCallingClientWithMultipleClients(t *testing.T) {
	for _, current := range []string{"local", "bridge"} {
		t.Run(current, func(t *testing.T) {
			m := tmux.NewMockTmux(t)
			m.EXPECT().IsAttached().Return(true)
			m.EXPECT().GetCurrentSession().Return(current, nil)
			m.EXPECT().ListSessions().Return([]*model.TmuxSession{
				{Name: "local", Path: "/local", Attached: 1},
				{Name: "bridge", Path: "/bridge", Attached: 1, SSHBridgeIdentity: "ssh/host/work"},
			}, nil)
			l := NewLister(model.Config{}, nil, m, nil, nil, nil, nil, nil)

			session, found := l.GetAttachedTmuxSession()

			if current == "bridge" {
				assert.False(t, found)
				assert.Equal(t, model.SeshSession{}, session)
			} else {
				require.True(t, found)
				assert.Equal(t, "local", session.Name)
			}
		})
	}
}

func TestListTmuxSessions(t *testing.T) {
	mockTmux := new(tmux.MockTmux)
	t.Run("should list tmux sessions", func(t *testing.T) {
		const timeFormat = "2006-01-02 15:04:05 -0700 MST"
		createdFA, _ := time.Parse(timeFormat, "2024-04-25 19:02:45 -0500 CDT")
		lastAttachedFA, _ := time.Parse(timeFormat, "2024-04-25 19:30:06 -0500 CDT")
		activityFA, _ := time.Parse(timeFormat, "2024-04-25 19:44:06 -0500 CDT")
		firstAttached := model.TmuxSession{
			Created:           &createdFA,
			LastAttached:      &lastAttachedFA,
			Activity:          &activityFA,
			Group:             "",
			Path:              "/Users/joshmedeski/c/sesh/main",
			Name:              "sesh/main",
			ID:                "$1",
			AttachedList:      []string{""},
			GroupList:         []string{""},
			GroupAttachedList: []string{""},
			Stack:             []int{2, 1},
			Alerts:            []string{},
			GroupSize:         0,
			GroupAttached:     0,
			Attached:          0,
			Windows:           2,
			Format:            true,
			GroupManyAttached: false,
			Grouped:           false,
			ManyAttached:      false,
			Marked:            false,
		}

		createdLA, _ := time.Parse(timeFormat, "2024-04-25 19:02:45 -0500 CDT")
		lastAttachedLA, _ := time.Parse(timeFormat, "2024-04-25 19:44:06 -0500 CDT")
		activityLA, _ := time.Parse(timeFormat, "2024-04-25 19:44:06 -0500 CDT")
		lastAttached := model.TmuxSession{
			Created:           &createdLA,
			LastAttached:      &lastAttachedLA,
			Activity:          &activityLA,
			Group:             "",
			Path:              "/Users/joshmedeski/c/sesh/v2",
			Name:              "sesh/v2",
			ID:                "$1",
			AttachedList:      []string{""},
			GroupList:         []string{""},
			GroupAttachedList: []string{""},
			Stack:             []int{2, 1},
			Alerts:            []string{},
			GroupSize:         0,
			GroupAttached:     0,
			Attached:          0,
			Windows:           2,
			Format:            true,
			GroupManyAttached: false,
			Grouped:           false,
			ManyAttached:      false,
			Marked:            false,
		}
		mockTmux.On("ListSessions").Return([]*model.TmuxSession{&firstAttached, &lastAttached}, nil)

		mockConfig := model.Config{}
		mockHome := new(home.MockHome)
		mockZoxide := new(zoxide.MockZoxide)
		mockTmuxinator := new(tmuxinator.MockTmuxinator)
		lister := NewLister(mockConfig, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil)

		realLister, ok := lister.(*RealLister)
		if !ok {
			log.Fatal("Cannot convert lister to *RealLister")
		}

		sessions, err := listTmux(realLister)
		assert.Equal(t, "tmux:sesh/main", sessions.OrderedIndex[0])
		assert.Equal(t, "sesh/main", sessions.Directory["tmux:sesh/main"].Name)
		assert.Equal(t, "tmux:sesh/v2", sessions.OrderedIndex[1])
		assert.Equal(t, nil, err)
	})
}

func makeTmuxSession(name, path string) *model.TmuxSession {
	return &model.TmuxSession{
		Name:    name,
		Path:    path,
		ID:      "$1",
		Windows: 1,
	}
}

func TestGetLastTmuxSession(t *testing.T) {
	tests := []struct {
		name      string
		sessions  []*model.TmuxSession
		blacklist []string
		wantName  string
		wantOk    bool
	}{
		{
			name: "no blacklist returns second session",
			sessions: []*model.TmuxSession{
				makeTmuxSession("current", "/tmp/current"),
				makeTmuxSession("previous", "/tmp/previous"),
				makeTmuxSession("oldest", "/tmp/oldest"),
			},
			blacklist: nil,
			wantName:  "previous",
			wantOk:    true,
		},
		{
			name: "second session blacklisted skips to next",
			sessions: []*model.TmuxSession{
				makeTmuxSession("current", "/tmp/current"),
				makeTmuxSession("blocked", "/tmp/blocked"),
				makeTmuxSession("fallback", "/tmp/fallback"),
			},
			blacklist: []string{"blocked"},
			wantName:  "fallback",
			wantOk:    true,
		},
		{
			name: "all sessions blacklisted returns false",
			sessions: []*model.TmuxSession{
				makeTmuxSession("a", "/tmp/a"),
				makeTmuxSession("b", "/tmp/b"),
			},
			blacklist: []string{"a", "b"},
			wantOk:    false,
		},
		{
			name: "only one non-blacklisted session returns false",
			sessions: []*model.TmuxSession{
				makeTmuxSession("keep", "/tmp/keep"),
				makeTmuxSession("blocked", "/tmp/blocked"),
			},
			blacklist: []string{"blocked"},
			wantOk:    false,
		},
		{
			name: "regex blacklist pattern matches",
			sessions: []*model.TmuxSession{
				makeTmuxSession("current", "/tmp/current"),
				makeTmuxSession("scratch-123", "/tmp/scratch-123"),
				makeTmuxSession("work", "/tmp/work"),
			},
			blacklist: []string{"^scratch-.*"},
			wantName:  "work",
			wantOk:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockTmux := new(tmux.MockTmux)
			mockTmux.On("ListSessions").Return(tt.sessions, nil)

			config := model.Config{Blacklist: tt.blacklist}
			lister := NewLister(config, new(home.MockHome), mockTmux, new(zoxide.MockZoxide), new(tmuxinator.MockTmuxinator), nil, nil, nil)

			session, ok := lister.GetLastTmuxSession()
			assert.Equal(t, tt.wantOk, ok)
			if tt.wantOk {
				assert.Equal(t, tt.wantName, session.Name)
			}
		})
	}
}

func TestListTmuxSessionsError(t *testing.T) {
	mockTmux := new(tmux.MockTmux)
	t.Run("should return error when unable to list tmux sessions", func(t *testing.T) {
		mockTmux.On("ListSessions").Return(nil, fmt.Errorf("some error"))

		mockConfig := model.Config{}
		mockHome := new(home.MockHome)
		mockZoxide := new(zoxide.MockZoxide)
		mockTmuxinator := new(tmuxinator.MockTmuxinator)
		lister := NewLister(mockConfig, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil)

		realLister, ok := lister.(*RealLister)
		if !ok {
			log.Fatal("Cannot convert lister to *RealLister")
		}

		sessions, err := listTmux(realLister)
		assert.Equal(t, model.SeshSessions{}, sessions)
		assert.NotNil(t, err)
		assert.Contains(t, err.Error(), "couldn't list tmux sessions")
	})
}

func TestListTmuxInheritsAliasFromConfig(t *testing.T) {
	mockTmux := new(tmux.MockTmux)
	mockTmux.On("ListSessions").Return([]*model.TmuxSession{
		makeTmuxSession("wallpaper", "/home/user/wallpaper"),
		makeTmuxSession("dotfiles", "/home/user/dotfiles"),
		makeTmuxSession("notes", "/home/user/notes"),
	}, nil)

	config := model.Config{
		SessionConfigs: []model.SessionConfig{
			{Name: "wallpaper", Alias: "wp"},
			{Name: "dotfiles", Alias: "DOT"},
		},
	}
	lister := NewLister(config, new(home.MockHome), mockTmux, new(zoxide.MockZoxide), new(tmuxinator.MockTmuxinator), nil, nil, nil)

	realLister, ok := lister.(*RealLister)
	if !ok {
		log.Fatal("Cannot convert lister to *RealLister")
	}

	sessions, err := listTmux(realLister)
	assert.Nil(t, err)
	assert.Equal(t, "wp", sessions.Directory["tmux:wallpaper"].Alias)
	assert.Equal(t, "DOT", sessions.Directory["tmux:dotfiles"].Alias)
	assert.Equal(t, "", sessions.Directory["tmux:notes"].Alias)
}

func TestAttachWindowNames(t *testing.T) {
	t.Run("fills in window names for tmux sessions only", func(t *testing.T) {
		sessions := model.SeshSessions{
			OrderedIndex: []string{"tmux:sesh", "config:sesh", "tmux:unknown"},
			Directory: model.SeshSessionMap{
				"tmux:sesh":    {Src: "tmux", Name: "sesh"},
				"config:sesh":  {Src: "config", Name: "sesh", WindowNames: []string{"configured"}},
				"tmux:unknown": {Src: "tmux", Name: "unknown"},
			},
		}
		attachWindowNames(sessions, map[string][]string{
			"sesh": {"editor", "server"},
		})

		assert.Equal(t, []string{"editor", "server"}, sessions.Directory["tmux:sesh"].WindowNames)
		assert.Equal(t, []string{"configured"}, sessions.Directory["config:sesh"].WindowNames,
			"config sessions keep their configured window names")
		assert.Nil(t, sessions.Directory["tmux:unknown"].WindowNames)
	})

	t.Run("no-op when there are no window names", func(t *testing.T) {
		sessions := model.SeshSessions{
			OrderedIndex: []string{"tmux:sesh"},
			Directory:    model.SeshSessionMap{"tmux:sesh": {Src: "tmux", Name: "sesh"}},
		}
		attachWindowNames(sessions, nil)
		assert.Nil(t, sessions.Directory["tmux:sesh"].WindowNames)
	})
}

func TestTmuxWindowNames(t *testing.T) {
	t.Run("returns nil when tmux fails", func(t *testing.T) {
		mockTmux := new(tmux.MockTmux)
		mockTmux.EXPECT().ListAllWindowNames("").Return(nil, assert.AnError)
		l := &RealLister{tmux: mockTmux}
		assert.Nil(t, tmuxWindowNames(l))
	})

	t.Run("returns the window names", func(t *testing.T) {
		mockTmux := new(tmux.MockTmux)
		format := "#{?#{pane_title},#{pane_title},#{window_name}}"
		mockTmux.EXPECT().ListAllWindowNames(format).Return(map[string][]string{"sesh": {"editor"}}, nil)
		l := &RealLister{
			config: model.Config{TUI: model.TUIConfig{WindowNameFormat: format}},
			tmux:   mockTmux,
		}
		assert.Equal(t, map[string][]string{"sesh": {"editor"}}, tmuxWindowNames(l))
	})
}
