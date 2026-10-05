package lister

import (
	"context"
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSSHConfiguredDefault(t *testing.T) {
	runner := &probeExec{result: execwrap.CommandResult{Stdout: []byte("0\nnutiliti/2345\n")}}
	config := model.Config{SSH: model.SSHConfig{Hosts: []string{"myinventa"}}}
	l := newSSHListTestLister(t, config, runner)

	sessions, err := l.List(ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"tmux:local", "tmuxinator:project", "zoxide:frequent", "ssh:ssh/myinventa/0", "ssh:ssh/myinventa/nutiliti%2F2345"}, sessions.OrderedIndex)
	assert.Equal(t, model.SeshSession{Src: "ssh", Name: "ssh/myinventa/0", Group: 4}, sessions.Directory["ssh:ssh/myinventa/0"])
	assert.Equal(t, "ssh", runner.name)
}

func TestListSSHAbsentOrEmptyPreservesLocalDefaults(t *testing.T) {
	for name, hosts := range map[string][]string{"absent": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			runner := &probeExec{result: execwrap.CommandResult{Stdout: []byte("unexpected\n")}}
			l := newSSHListTestLister(t, model.Config{SSH: model.SSHConfig{Hosts: hosts}}, runner)

			sessions, err := l.List(ListOptions{})
			require.NoError(t, err)
			assert.Equal(t, []string{"tmux:local", "tmuxinator:project", "zoxide:frequent"}, sessions.OrderedIndex)
			assert.Equal(t, model.SeshSession{Src: "tmux", Name: "local", Path: "/local"}, sessions.Directory["tmux:local"])
			assert.Equal(t, model.SeshSession{Src: "tmuxinator", Name: "project", Group: 2}, sessions.Directory["tmuxinator:project"])
			assert.Equal(t, model.SeshSession{Src: "zoxide", Name: "frequent", Path: "/frequent", Score: 10, Group: 3}, sessions.Directory["zoxide:frequent"])
			assert.Empty(t, runner.name, "local defaults must not execute SSH")
		})
	}
}

func TestListSSHSourceFilters(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts ListOptions
		want []string
	}{
		{"tmux only", ListOptions{Tmux: true}, []string{"tmux:local"}},
		{"config only", ListOptions{Config: true}, []string{}},
		{"tmuxinator only", ListOptions{Tmuxinator: true}, []string{"tmuxinator:project"}},
		{"zoxide only", ListOptions{Zoxide: true}, []string{"zoxide:frequent"}},
		{"local union", ListOptions{Tmux: true, Zoxide: true}, []string{"tmux:local", "zoxide:frequent"}},
		{"SSH only", ListOptions{SSH: true}, []string{"ssh:ssh/server/0"}},
		{"SSH and tmux", ListOptions{SSH: true, Tmux: true}, []string{"tmux:local", "ssh:ssh/server/0"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &probeExec{result: execwrap.CommandResult{Stdout: []byte("0\n")}}
			l := newSSHListTestLister(t, model.Config{SSH: model.SSHConfig{Hosts: []string{"server"}}}, runner)

			sessions, err := l.List(tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, sessions.OrderedIndex)
			if tt.opts.SSH {
				assert.Equal(t, "ssh", runner.name)
			} else {
				assert.Empty(t, runner.name, "local filters must not execute SSH")
			}
		})
	}
}

func TestListSSHConfiguredOrderAndSortOrder(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config model.SortOrder
		opts   model.SortOrder
		want   []string
		groups []int
	}{
		{"flat", model.SortOrder{"SSH", "tmux"}, nil,
			[]string{"ssh:ssh/first/z", "ssh:ssh/first/a", "ssh:ssh/second/0", "tmux:local"}, []int{0, 0, 0, 1}},
		{"merged", model.SortOrder{[]string{"ssh", "tmux"}}, nil,
			[]string{"ssh:ssh/first/z", "ssh:ssh/first/a", "ssh:ssh/second/0", "tmux:local"}, []int{0, 0, 0, 0}},
		{"option overrides config", model.SortOrder{"ssh", "tmux"}, model.SortOrder{"tmux", "ssh"},
			[]string{"tmux:local", "ssh:ssh/first/z", "ssh:ssh/first/a", "ssh:ssh/second/0"}, []int{0, 1, 1, 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hosts := []string{"first", "second"}
			runner := newControlledSSHExec(hosts)
			runner.release["first"] = make(chan struct{})
			runner.responses["first"] = execResponse{result: execwrap.CommandResult{Stdout: []byte("z\na\n")}}
			runner.responses["second"] = execResponse{result: execwrap.CommandResult{Stdout: []byte("0\n")}}
			l := newSSHListTestLister(t, model.Config{SSH: model.SSHConfig{Hosts: hosts}, SortOrder: tt.config}, runner)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			type result struct {
				sessions model.SeshSessions
				err      error
			}
			done := make(chan result, 1)
			go func() {
				sessions, err := l.List(ListOptions{Context: ctx, SSH: true, Tmux: true, SortOrder: tt.opts})
				done <- result{sessions, err}
			}()

			// The second host completes before the first is released.
			require.Equal(t, "second", waitSSHEvent(t, runner.completed))
			close(runner.release["first"])
			got := waitSSHEvent(t, done)
			require.NoError(t, got.err)
			assert.Equal(t, tt.want, got.sessions.OrderedIndex)
			for i, key := range got.sessions.OrderedIndex {
				assert.Equal(t, tt.groups[i], got.sessions.Directory[key].Group)
			}
		})
	}
}

func TestListSSHFailureAggregation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		opts      ListOptions
		responses map[string]execResponse
		want      []string
		wantError bool
	}{
		{"default all failed", ListOptions{}, map[string]execResponse{
			"first": {err: errors.New("offline")}, "second": {err: errors.New("offline")},
		}, []string{"tmux:local", "tmuxinator:project", "zoxide:frequent"}, false},
		{"mixed partial failure", ListOptions{Tmux: true, SSH: true}, map[string]execResponse{
			"first": {err: errors.New("offline")}, "second": {result: execwrap.CommandResult{Stdout: []byte("0\n")}},
		}, []string{"tmux:local", "ssh:ssh/second/0"}, false},
		{"SSH partial failure", ListOptions{SSH: true}, map[string]execResponse{
			"first": {err: errors.New("offline")}, "second": {result: execwrap.CommandResult{Stdout: []byte("0\n")}},
		}, []string{"ssh:ssh/second/0"}, false},
		{"SSH successful empty host", ListOptions{SSH: true}, map[string]execResponse{
			"first": {err: errors.New("offline")}, "second": {result: execwrap.CommandResult{}},
		}, []string{}, false},
		{"SSH all failed", ListOptions{SSH: true}, map[string]execResponse{
			"first": {err: errors.New("offline")}, "second": {err: errors.New("offline")},
		}, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hosts := []string{"first", "second"}
			runner := newControlledSSHExec(hosts)
			runner.responses = tt.responses
			l := newSSHListTestLister(t, model.Config{SSH: model.SSHConfig{Hosts: hosts}}, runner)

			sessions, err := l.List(tt.opts)
			if tt.wantError {
				require.ErrorContains(t, err, "all configured SSH hosts failed")
				assert.Contains(t, err.Error(), "first")
				assert.Contains(t, err.Error(), "second")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, sessions.OrderedIndex)
		})
	}
}

func TestListSSHCancellation(t *testing.T) {
	hosts := []string{"first", "second"}
	runner := newControlledSSHExec(hosts)
	runner.blockOnContext = true
	l := newSSHListTestLister(t, model.Config{SSH: model.SSHConfig{Hosts: hosts}}, runner)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		_, err := l.List(ListOptions{Context: ctx})
		done <- err
	}()
	started := []string{waitSSHEvent(t, runner.started), waitSSHEvent(t, runner.started)}
	require.ElementsMatch(t, hosts, started)
	cancel()
	require.ErrorIs(t, waitSSHEvent(t, done), context.Canceled)
	for range hosts {
		assert.ErrorIs(t, waitSSHEvent(t, runner.stopped).err, context.Canceled)
	}
	assert.ElementsMatch(t, hosts, []string{waitSSHEvent(t, runner.completed), waitSSHEvent(t, runner.completed)})
}

func TestListSSHOnlyWithoutHosts(t *testing.T) {
	runner := &probeExec{result: execwrap.CommandResult{Stdout: []byte("unexpected\n")}}
	l := newSSHListTestLister(t, model.Config{}, runner)
	sessions, err := l.List(ListOptions{SSH: true})
	require.NoError(t, err)
	assert.Empty(t, sessions.OrderedIndex)
	assert.Empty(t, sessions.Directory)
	assert.Empty(t, runner.name, "an empty host list must not execute SSH")
}

func newSSHListTestLister(t *testing.T, config model.Config, runner execwrap.Exec) Lister {
	t.Helper()
	mockTmux := new(tmux.MockTmux)
	mockTmux.On("ListSessions").Return([]*model.TmuxSession{{Name: "local", Path: "/local"}}, nil)
	mockTmuxinator := new(tmuxinator.MockTmuxinator)
	mockTmuxinator.On("List").Return([]*model.TmuxinatorConfig{{Name: "project"}}, nil)
	mockZoxide := new(zoxide.MockZoxide)
	mockZoxide.On("ListResults").Return([]*model.ZoxideResult{{Path: "/frequent", Score: 10}}, nil)
	mockHome := new(home.MockHome)
	mockHome.On("ShortenHome", "/frequent").Return("frequent", nil)
	return NewLister(config, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, runner)
}

func TestHideDuplicates(t *testing.T) {
	tests := []struct {
		name              string
		tmuxSessions      []*model.TmuxSession
		zoxideResults     []*model.ZoxideResult
		configSessions    []model.SessionConfig
		tmuxinatorConfigs []*model.TmuxinatorConfig
		homeShortenHome   map[string]string
		expectedNames     []string
	}{
		{
			name: "no duplicates",
			tmuxSessions: []*model.TmuxSession{
				{Name: "session1", Path: "/path/to/session1"},
			},
			zoxideResults: []*model.ZoxideResult{
				{Path: "/path/to/session2", Score: 1.0},
			},
			homeShortenHome: map[string]string{
				"/path/to/session2": "session2",
			},
			expectedNames: []string{"session1", "session2"},
		},
		{
			name: "name_collision_kept_when_paths_differ",
			tmuxSessions: []*model.TmuxSession{
				{Name: "dev", Path: "/path/to/dev1"},
			},
			zoxideResults: []*model.ZoxideResult{
				{Path: "/path/to/dev2", Score: 1.0},
			},
			homeShortenHome: map[string]string{
				"/path/to/dev2": "dev",
			},
			expectedNames: []string{"dev", "dev"},
		},
		{
			name: "path duplicates only",
			tmuxSessions: []*model.TmuxSession{
				{Name: "dev1", Path: "/path/to/dev"},
			},
			zoxideResults: []*model.ZoxideResult{
				{Path: "/path/to/dev", Score: 1.0},
			},
			homeShortenHome: map[string]string{
				"/path/to/dev": "dev2",
			},
			expectedNames: []string{"dev1"},
		},
		{
			name: "tmuxinator_and_zoxide_kept_no_rule_between_them",
			tmuxinatorConfigs: []*model.TmuxinatorConfig{
				{Name: "dev"},
			},
			zoxideResults: []*model.ZoxideResult{
				{Path: "/path/to/dev", Score: 1.0},
			},
			homeShortenHome: map[string]string{
				"/path/to/dev": "dev",
			},
			expectedNames: []string{"dev", "dev"},
		},
		{
			name: "order preservation",
			tmuxSessions: []*model.TmuxSession{
				{Name: "a", Path: "/path/to/a"},
			},
			zoxideResults: []*model.ZoxideResult{
				{Path: "/path/to/b", Score: 1.0},
			},
			configSessions: []model.SessionConfig{
				{Name: "a", Path: "/path/to/c"},
			},
			homeShortenHome: map[string]string{
				"/path/to/b": "b",
			},
			expectedNames: []string{"a", "b"},
		},
		{
			name:              "empty input",
			tmuxSessions:      []*model.TmuxSession{},
			zoxideResults:     []*model.ZoxideResult{},
			configSessions:    []model.SessionConfig{},
			tmuxinatorConfigs: []*model.TmuxinatorConfig{},
			expectedNames:     []string{},
		},
		{
			name: "single session",
			tmuxSessions: []*model.TmuxSession{
				{Name: "single", Path: "/path/to/single"},
			},
			expectedNames: []string{"single"},
		},
		{
			name: "all duplicates",
			tmuxSessions: []*model.TmuxSession{
				{Name: "dup", Path: "/path/to/dup"},
			},
			zoxideResults: []*model.ZoxideResult{
				{Path: "/path/to/dup", Score: 1.0},
			},
			configSessions: []model.SessionConfig{
				{Name: "dup", Path: "/path/to/dup"},
			},
			homeShortenHome: map[string]string{
				"/path/to/dup": "dup",
			},
			expectedNames: []string{"dup"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockTmux := new(tmux.MockTmux)
			mockZoxide := new(zoxide.MockZoxide)
			mockHome := new(home.MockHome)
			mockTmuxinator := new(tmuxinator.MockTmuxinator)

			// Set up mocks for all sources that have data (including empty data)
			if tt.tmuxSessions != nil {
				mockTmux.On("ListSessions").Return(tt.tmuxSessions, nil)
			}
			if tt.zoxideResults != nil {
				mockZoxide.On("ListResults").Return(tt.zoxideResults, nil)
				for path, shortened := range tt.homeShortenHome {
					mockHome.On("ShortenHome", path).Return(shortened, nil)
				}
			}
			if tt.configSessions != nil {
				for _, session := range tt.configSessions {
					mockHome.On("ExpandPath", session.Path).Return(session.Path, nil)
				}
			}
			if tt.tmuxinatorConfigs != nil {
				mockTmuxinator.On("List").Return(tt.tmuxinatorConfigs, nil)
			}

			config := model.Config{
				SessionConfigs: tt.configSessions,
			}

			lister := NewLister(config, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil)

			// Call the actual List function with HideDuplicates
			result, err := lister.List(ListOptions{
				Tmux:           tt.tmuxSessions != nil,
				Zoxide:         tt.zoxideResults != nil,
				Config:         tt.configSessions != nil,
				Tmuxinator:     tt.tmuxinatorConfigs != nil,
				HideDuplicates: true,
			})

			assert.NoError(t, err)
			assert.Equal(t, len(tt.expectedNames), len(result.OrderedIndex))

			for i, expectedName := range tt.expectedNames {
				if i < len(result.OrderedIndex) {
					session := result.Directory[result.OrderedIndex[i]]
					assert.Equal(t, expectedName, session.Name)
				}
			}

			// Verify all mocks were called
			mockTmux.AssertExpectations(t)
			mockZoxide.AssertExpectations(t)
			mockTmuxinator.AssertExpectations(t)
		})
	}
}

func TestHideAttachedBeforeHideDuplicates(t *testing.T) {
	mockTmux := new(tmux.MockTmux)
	mockTmux.On("IsAttached").Return(true)
	mockTmux.On("GetCurrentSession").Return("project", nil)
	mockZoxide := new(zoxide.MockZoxide)
	mockHome := new(home.MockHome)
	mockTmuxinator := new(tmuxinator.MockTmuxinator)

	mockTmux.On("ListSessions").Return([]*model.TmuxSession{
		{Name: "project", Path: "/p", Attached: 1},
	}, nil)
	mockHome.On("ExpandPath", "/p").Return("/p", nil)

	config := model.Config{
		SessionConfigs: []model.SessionConfig{{Name: "project", Path: "/p"}},
	}
	l := NewLister(config, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil)

	result, err := l.List(ListOptions{
		Tmux:           true,
		Config:         true,
		HideAttached:   true,
		HideDuplicates: true,
	})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(result.OrderedIndex))
	kept := result.Directory[result.OrderedIndex[0]]
	assert.Equal(t, "config", kept.Src)
	assert.Equal(t, "project", kept.Name)
}

func TestBlacklistedFlag(t *testing.T) {
	tests := []struct {
		name          string
		blacklist     []string
		tmuxSessions  []*model.TmuxSession
		blacklisted   bool
		expectedNames []string
	}{
		{
			name:      "flag on, some sessions match patterns",
			blacklist: []string{"^scratch$", "^temp"},
			tmuxSessions: []*model.TmuxSession{
				{Name: "scratch", Path: "/path/to/scratch"},
				{Name: "work", Path: "/path/to/work"},
				{Name: "temporary", Path: "/path/to/temporary"},
			},
			blacklisted:   true,
			expectedNames: []string{"scratch", "temporary"},
		},
		{
			name:      "flag on, no sessions match patterns",
			blacklist: []string{"^nomatch$"},
			tmuxSessions: []*model.TmuxSession{
				{Name: "alpha", Path: "/path/to/alpha"},
				{Name: "beta", Path: "/path/to/beta"},
			},
			blacklisted:   true,
			expectedNames: []string{},
		},
		{
			name:      "flag on, empty blacklist in config",
			blacklist: []string{},
			tmuxSessions: []*model.TmuxSession{
				{Name: "alpha", Path: "/path/to/alpha"},
			},
			blacklisted:   true,
			expectedNames: []string{},
		},
		{
			name:      "flag off preserves existing hide-blacklisted behavior",
			blacklist: []string{"^scratch$"},
			tmuxSessions: []*model.TmuxSession{
				{Name: "scratch", Path: "/path/to/scratch"},
				{Name: "work", Path: "/path/to/work"},
			},
			blacklisted:   false,
			expectedNames: []string{"work"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockTmux := new(tmux.MockTmux)
			mockZoxide := new(zoxide.MockZoxide)
			mockHome := new(home.MockHome)
			mockTmuxinator := new(tmuxinator.MockTmuxinator)

			mockTmux.On("ListSessions").Return(tt.tmuxSessions, nil)

			config := model.Config{Blacklist: tt.blacklist}
			lister := NewLister(config, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil)

			result, err := lister.List(ListOptions{
				Tmux:        true,
				Blacklisted: tt.blacklisted,
			})

			assert.NoError(t, err)
			assert.Equal(t, len(tt.expectedNames), len(result.OrderedIndex))
			for i, expectedName := range tt.expectedNames {
				if i < len(result.OrderedIndex) {
					session := result.Directory[result.OrderedIndex[i]]
					assert.Equal(t, expectedName, session.Name)
				}
			}

			mockTmux.AssertExpectations(t)
		})
	}
}

func TestList_ShowWindows(t *testing.T) {
	newLister := func(showWindows bool, mockTmux *tmux.MockTmux) Lister {
		config := model.Config{}
		config.TUI.ShowWindows = showWindows
		return NewLister(config, new(home.MockHome), mockTmux, new(zoxide.MockZoxide), new(tmuxinator.MockTmuxinator), nil, nil, nil)
	}

	t.Run("attaches window names when enabled", func(t *testing.T) {
		mockTmux := new(tmux.MockTmux)
		mockTmux.On("ListSessions").Return([]*model.TmuxSession{
			{Name: "sesh", Path: "/p"},
		}, nil)
		mockTmux.EXPECT().ListAllWindowNames("").Return(map[string][]string{
			"sesh": {"editor", "server"},
		}, nil).Once()

		result, err := newLister(true, mockTmux).List(ListOptions{Tmux: true})
		assert.NoError(t, err)
		session := result.Directory[result.OrderedIndex[0]]
		assert.Equal(t, []string{"editor", "server"}, session.WindowNames)
		// One tmux call for all sessions, not one per session.
		mockTmux.AssertExpectations(t)
	})

	t.Run("does not call tmux when disabled", func(t *testing.T) {
		mockTmux := new(tmux.MockTmux)
		mockTmux.On("ListSessions").Return([]*model.TmuxSession{
			{Name: "sesh", Path: "/p"},
		}, nil)

		result, err := newLister(false, mockTmux).List(ListOptions{Tmux: true})
		assert.NoError(t, err)
		session := result.Directory[result.OrderedIndex[0]]
		assert.Nil(t, session.WindowNames)
		mockTmux.AssertNotCalled(t, "ListAllWindowNames")
	})

	t.Run("does not call tmux when tmux is not a listed source", func(t *testing.T) {
		mockTmux := new(tmux.MockTmux)

		_, err := newLister(true, mockTmux).List(ListOptions{Config: true})
		assert.NoError(t, err)
		mockTmux.AssertNotCalled(t, "ListAllWindowNames")
	})
}

func TestMergedSortOrderGroup(t *testing.T) {
	mockTmux := new(tmux.MockTmux)
	mockZoxide := new(zoxide.MockZoxide)
	mockHome := new(home.MockHome)
	mockTmuxinator := new(tmuxinator.MockTmuxinator)

	mockTmux.On("ListSessions").Return([]*model.TmuxSession{
		{Name: "live", Path: "/live"},
	}, nil)
	mockZoxide.On("ListResults").Return([]*model.ZoxideResult{
		{Path: "/hot", Score: 90},
		{Path: "/notes", Score: 20},
		{Path: "/cold", Score: 1},
	}, nil)
	for path, short := range map[string]string{"/hot": "hot", "/notes": "notes", "/cold": "cold"} {
		mockHome.On("ShortenHome", path).Return(short, nil)
	}
	mockHome.On("ExpandPath", "/notes").Return("/notes", nil)
	mockHome.On("ExpandPath", "/never").Return("/never", nil)

	config := model.Config{
		// "notes" is a config session zoxide also knows: it should sort by the
		// score zoxide has for its path, not by coming from the config source.
		SessionConfigs: []model.SessionConfig{
			{Name: "notes-cfg", Path: "/notes"},
			{Name: "never-cfg", Path: "/never"},
		},
		SortOrder: model.SortOrder{"tmux", []string{"config", "zoxide"}},
	}
	l := NewLister(config, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil)

	result, err := l.List(ListOptions{Tmux: true, Config: true, Zoxide: true})
	assert.NoError(t, err)

	names := make([]string, 0, len(result.OrderedIndex))
	groups := make([]int, 0, len(result.OrderedIndex))
	for _, key := range result.OrderedIndex {
		names = append(names, result.Directory[key].Name)
		groups = append(groups, result.Directory[key].Group)
	}

	// tmux stays pinned in its own group; config and zoxide interleave by
	// score, and the config session zoxide has never seen trails the group.
	assert.Equal(t, []string{"live", "hot", "notes-cfg", "notes", "cold", "never-cfg"}, names)
	assert.Equal(t, []int{0, 1, 1, 1, 1, 1}, groups)
}

func TestListOptionsSortOrderOverridesConfig(t *testing.T) {
	mockTmux := new(tmux.MockTmux)
	mockZoxide := new(zoxide.MockZoxide)
	mockHome := new(home.MockHome)
	mockTmuxinator := new(tmuxinator.MockTmuxinator)

	mockTmux.On("ListSessions").Return([]*model.TmuxSession{{Name: "live", Path: "/live"}}, nil)
	mockZoxide.On("ListResults").Return([]*model.ZoxideResult{
		{Path: "/hot", Score: 90},
		{Path: "/cold", Score: 1},
	}, nil)
	mockHome.On("ShortenHome", "/hot").Return("hot", nil)
	mockHome.On("ShortenHome", "/cold").Return("cold", nil)
	mockHome.On("ExpandPath", "/hot").Return("/hot", nil)

	config := model.Config{
		SessionConfigs: []model.SessionConfig{{Name: "notes-cfg", Path: "/hot"}},
		SortOrder:      model.SortOrder{"zoxide", "config", "tmux"},
	}
	l := NewLister(config, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil)

	result, err := l.List(ListOptions{
		Tmux: true, Config: true, Zoxide: true,
		SortOrder: model.SortOrder{"tmux", []any{"config", "zoxide"}},
	})
	assert.NoError(t, err)

	names := make([]string, 0, len(result.OrderedIndex))
	groups := make([]int, 0, len(result.OrderedIndex))
	for _, key := range result.OrderedIndex {
		names = append(names, result.Directory[key].Name)
		groups = append(groups, result.Directory[key].Group)
	}
	assert.Equal(t, []string{"live", "notes-cfg", "hot", "cold"}, names)
	assert.Equal(t, []int{0, 1, 1, 1}, groups)
}

func TestFlatSortOrderKeepsSourceBlocks(t *testing.T) {
	mockTmux := new(tmux.MockTmux)
	mockZoxide := new(zoxide.MockZoxide)
	mockHome := new(home.MockHome)
	mockTmuxinator := new(tmuxinator.MockTmuxinator)

	mockZoxide.On("ListResults").Return([]*model.ZoxideResult{
		{Path: "/hot", Score: 90},
	}, nil)
	mockHome.On("ShortenHome", "/hot").Return("hot", nil)
	mockHome.On("ExpandPath", "/never").Return("/never", nil)

	config := model.Config{
		SessionConfigs: []model.SessionConfig{{Name: "never-cfg", Path: "/never"}},
		SortOrder:      model.SortOrder{"config", "zoxide"},
	}
	l := NewLister(config, mockHome, mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil)

	result, err := l.List(ListOptions{Config: true, Zoxide: true})
	assert.NoError(t, err)

	names := make([]string, 0, len(result.OrderedIndex))
	groups := make([]int, 0, len(result.OrderedIndex))
	for _, key := range result.OrderedIndex {
		names = append(names, result.Directory[key].Name)
		groups = append(groups, result.Directory[key].Group)
	}
	// Unmerged, the unscored config session still leads its own block.
	assert.Equal(t, []string{"never-cfg", "hot"}, names)
	assert.Equal(t, []int{0, 1}, groups)
}
