package picker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/model"
)

var ctrlS = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}

func sshPickerSessions() model.SeshSessions {
	return model.SeshSessions{
		OrderedIndex: []string{"config1", "local1", "zoxide", "remote1", "local2", "remote2", "tmuxinator", "config2"},
		Directory: model.SeshSessionMap{
			"config1":    {Src: "config", Name: "configured", Group: 0},
			"local1":     {Src: "tmux", Name: "development", Path: "/development", TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration, Group: 1},
			"zoxide":     {Src: "zoxide", Name: "~/projects", Path: "/projects", Group: 2},
			"remote1":    {Src: "ssh", Name: "ssh/myinventa/myinventa", Group: 3},
			"local2":     {Src: "tmux", Name: "infrastructure", Path: "/infrastructure", TmuxSessionID: "$2", TmuxServerGeneration: testServerGeneration, Group: 1},
			"remote2":    {Src: "ssh", Name: "ssh/myinventa/deploy", Group: 3},
			"tmuxinator": {Src: "tmuxinator", Name: "rails", Group: 4},
			"config2":    {Src: "config", Name: "other-config", Group: 0},
		},
	}
}

func loadSSHModel(sessions model.SeshSessions, opts Options) Model {
	m := New(testFetchFunc(sessions), opts)
	loaded, _ := m.Update(m.fetchSessions()())
	m = loaded.(Model)
	m.width, m.height = 80, 24
	return m
}

func TestSSHSourceOrderingIsPickerOnlyAndStable(t *testing.T) {
	sessions := sshPickerSessions()
	before := append([]string(nil), sessions.OrderedIndex...)
	m := loadSSHModel(sessions, testOptions())
	want := []string{"remote1", "remote2", "local1", "local2", "config1", "zoxide", "tmuxinator", "config2"}
	for i, key := range want {
		require.Equal(t, sessions.Directory[key], m.filtered[i].item.session, "row metadata and category order")
	}
	require.Equal(t, before, sessions.OrderedIndex, "the global listing order is not rewritten")
	for _, key := range []string{"remote1", "remote2"} {
		require.Empty(t, sessions.Directory[key].Path)
	}
}

func TestSSHSourceOrderingPreservesNoSSHInventory(t *testing.T) {
	sessions := testSessions()
	m := loadSSHModel(sessions, testOptions())
	for i, key := range sessions.OrderedIndex {
		require.Equal(t, sessions.Directory[key], m.filtered[i].item.session)
	}
}

func TestSSHSourceFilterCycle(t *testing.T) {
	sessions := sshPickerSessions()
	fetches := 0
	m := New(func() (model.SeshSessions, error) { fetches++; return sessions, nil }, testOptions())
	loaded, _ := m.Update(m.fetchSessions()())
	m = loaded.(Model)
	m.width, m.height = 80, 24
	all := filteredNames(m)
	for _, source := range []string{"ssh", "tmux", "config", "zoxide", "tmuxinator", ""} {
		m.cursor, m.offset = len(m.filtered)-1, 2
		m = press(m, ctrlS)
		require.Equal(t, source, m.sourceFilter)
		require.Zero(t, m.cursor)
		require.Zero(t, m.offset)
		for _, item := range m.filtered {
			if source != "" {
				require.Equal(t, source, item.item.session.Src)
			}
		}
		label := strings.ToUpper(source)
		if label == "" {
			label = "ALL"
			require.Equal(t, all, filteredNames(m))
		}
		require.Contains(t, ansi.Strip(m.View().Content), "Source: "+label+" · Ctrl-s")
	}
	require.Equal(t, 1, fetches, "source cycling must not refetch or probe SSH")
}

func TestSSHSourceFilterSkipsAbsentSources(t *testing.T) {
	for _, source := range []string{"ssh", "tmux", "config", "zoxide", "tmuxinator", "other"} {
		t.Run(source, func(t *testing.T) {
			sessions := model.SeshSessions{OrderedIndex: []string{"only"}, Directory: model.SeshSessionMap{
				"only": {Src: source, Name: "one"},
			}}
			m := loadSSHModel(sessions, testOptions())
			m = press(m, ctrlS)
			if source == "other" {
				require.Empty(t, m.sourceFilter)
			} else {
				require.Equal(t, source, m.sourceFilter)
			}
			require.Len(t, m.filtered, 1)
			m = press(m, ctrlS)
			require.Empty(t, m.sourceFilter)
			require.Len(t, m.filtered, 1)
		})
	}
	m := New(testFetchFunc(sshPickerSessions()), testOptions())
	m = press(m, ctrlS)
	require.True(t, m.loading)
	require.Empty(t, m.sourceFilter)
}

func TestSSHSourceFilterPreservesSearchAndRelevance(t *testing.T) {
	m := loadSSHModel(sshPickerSessions(), testOptions())
	m, _ = typeFilter(m, "de")
	require.Equal(t, []string{"development", "ssh/myinventa/deploy"}, filteredNames(m), "stronger local match stays above SSH")
	m = press(m, ctrlS)
	require.Equal(t, "de", m.filterInput.Value())
	require.Equal(t, []string{"ssh/myinventa/deploy"}, filteredNames(m))
	m = press(m, ctrlS)
	require.Equal(t, "de", m.filterInput.Value())
	require.Equal(t, []string{"development"}, filteredNames(m))
	for m.sourceFilter != "" {
		m = press(m, ctrlS)
	}
	require.Equal(t, []string{"development", "ssh/myinventa/deploy"}, filteredNames(m))
	for _, item := range m.filtered {
		require.NotEmpty(t, item.matchedIndexes)
	}
}

func TestSSHSourceOrderOnlyBreaksEqualSearchScores(t *testing.T) {
	const name = "ssh/myinventa/deploy"
	sessions := model.SeshSessions{
		OrderedIndex: []string{"local", "remote"},
		Directory: model.SeshSessionMap{
			"local":  {Src: "tmux", Name: name, Path: "/local", TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration},
			"remote": {Src: "ssh", Name: name},
		},
	}
	m := loadSSHModel(sessions, testOptions())
	m, _ = typeFilter(m, "deploy")
	require.Len(t, m.filtered, 2)
	require.Equal(t, "ssh", m.filtered[0].item.src)
	require.Equal(t, "tmux", m.filtered[1].item.src)
	m = press(m, ctrlS)
	require.Len(t, m.filtered, 1, "filter by source, not the ssh/ name prefix")
	require.Equal(t, sessions.Directory["remote"], m.filtered[0].item.session)
	m = press(m, ctrlS)
	require.Len(t, m.filtered, 1)
	require.Equal(t, sessions.Directory["local"], m.filtered[0].item.session)
}

func TestSSHSourceSelectionPreservesCanonicalIdentity(t *testing.T) {
	for _, selection := range []string{"enter", "index"} {
		t.Run(selection, func(t *testing.T) {
			sessions := sshPickerSessions()
			row := sessions.Directory["remote1"]
			row.Name = "ssh/myinventa/deploy%2Fblue%20caf%C3%A9"
			sessions.Directory["remote1"] = row
			m := loadSSHModel(sessions, testOptionsWith(func(o *Options) {
				o.ShowIcons, o.SeparatorAware = true, true
			}))
			m = press(m, ctrlS)
			require.Contains(t, ansi.Strip(m.View().Content), "⚡ myinventa — deploy/blue café")
			require.NotContains(t, ansi.Strip(m.View().Content), row.Name)
			if selection == "index" {
				m, _ = typeFilter(m, "#")
				m = press(m, tea.KeyPressMsg{Code: '1', Text: "1"})
			} else {
				m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			require.Equal(t, row, m.ChosenSession())
			require.Empty(t, m.ChosenSession().Path)
		})
	}
}

func TestSSHSourceFilterAppliesToAliasesAndPendingAutoConnect(t *testing.T) {
	for _, mode := range []string{"normal", "alias"} {
		t.Run(mode, func(t *testing.T) {
			m := loadSSHModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
				o.Aliases = map[string]Alias{"d": {Alias: "d", Target: "development", AutoConnect: true}}
				o.AliasAutoConnectDelay = 0
			}))
			query := "d"
			if mode == "alias" {
				query = "/d"
			}
			m, cmd := typeFilter(m, query)
			tick := aliasTick(cmd)
			require.NotNil(t, tick)
			m = press(m, ctrlS)
			require.Equal(t, "ssh", m.sourceFilter)
			m = press(m, *tick)
			require.Empty(t, m.ChosenSession(), "a pending local alias cannot bypass SSH filtering")
			_, allowed := m.exactAlias(query)
			require.False(t, allowed)
			if mode == "normal" {
				require.Equal(t, []string{"ssh/myinventa/deploy"}, filteredNames(m), "a hidden alias does not suppress SSH fuzzy matches")
			} else {
				require.Empty(t, m.filtered)
			}
			m = press(m, ctrlS)
			_, allowed = m.exactAlias(query)
			require.True(t, allowed, "the same alias remains usable in its source")
		})
	}
}

func TestSSHSourceFilterKeepsUnlistedConfigAliasesInAll(t *testing.T) {
	m := loadSSHModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
		o.Aliases = map[string]Alias{"a": {Alias: "a", Target: "archive", AutoConnect: true}}
	}))
	m.filterInput.SetValue("/a")
	m.applyFilter()
	require.Equal(t, []string{"archive"}, filteredNames(m))
	m = press(m, ctrlS)
	require.Empty(t, m.filtered)
	m = press(m, ctrlS)
	require.Empty(t, m.filtered)
	m = press(m, ctrlS)
	require.Equal(t, "config", m.sourceFilter)
	require.Equal(t, []string{"archive"}, filteredNames(m))
	for m.sourceFilter != "" {
		m = press(m, ctrlS)
	}
	require.Equal(t, []string{"archive"}, filteredNames(m))
}

func TestSSHSourceFilterResolvesAliasWithinSelectedSource(t *testing.T) {
	const name = "ssh/myinventa/deploy"
	sessions := model.SeshSessions{
		OrderedIndex: []string{"local", "remote", "configured"},
		Directory: model.SeshSessionMap{
			"local":      {Src: "tmux", Name: name, Path: "/local", TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration},
			"remote":     {Src: "ssh", Name: name},
			"configured": {Src: "config", Name: name, Path: "/configured"},
		},
	}
	for i, key := range []string{"remote", "local", "configured"} {
		for _, selection := range []string{"enter", "auto"} {
			t.Run(key+"/"+selection, func(t *testing.T) {
				m := loadSSHModel(sessions, testOptionsWith(func(o *Options) {
					o.Aliases = map[string]Alias{"w": {Alias: "w", Target: name, AutoConnect: true}}
					o.AliasAutoConnectDelay = 0
				}))
				for step := 0; step <= i; step++ {
					m = press(m, ctrlS)
				}
				m, cmd := typeFilter(m, "/w")
				require.Len(t, m.filtered, 1)
				require.Equal(t, sessions.Directory[key], m.filtered[0].item.session)
				if selection == "auto" {
					tick := aliasTick(cmd)
					require.NotNil(t, tick)
					m = press(m, *tick)
				} else {
					m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				}
				require.Equal(t, sessions.Directory[key], m.ChosenSession())
			})
		}
	}
}
