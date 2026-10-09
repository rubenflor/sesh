package picker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/icon"
	"github.com/joshmedeski/sesh/v2/model"
)

func TestSSHDisplayRows(t *testing.T) {
	for _, test := range []struct{ host, session string }{
		{"myinventa", "myinventa"},
		{"myinventa", "deploy"},
		{"bookstack", "admin"},
		{"server1", "production"},
		{"server2", "production"},
		{"host", "two words: deploy/blue"},
		{"host", `quotes ' " \ $1 $(echo unsafe) ` + "`echo unsafe` ; *?[#]"},
		{"host", "café 東京 ⚙"},
		{"host", "literal%2F%20"},
		{"host/alias café", "deploy"},
	} {
		t.Run(test.host+"/"+test.session, func(t *testing.T) {
			identity, err := model.EncodeSSHIdentity(test.host, test.session)
			require.NoError(t, err)
			row := model.SeshSession{Src: "ssh", Name: identity, PreviewCommand: "preview", Group: 3}
			sessions := model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{"remote": row}}
			for _, icons := range []bool{true, false} {
				m := loadSSHModel(sessions, testOptionsWith(func(o *Options) { o.ShowIcons = icons }))
				m.width = 160
				label := test.host + " — " + test.session
				prefix := "> "
				if icons {
					prefix += "⚡ "
				}
				require.Equal(t, prefix+label, iconRow(t, m, label))
				require.NotContains(t, ansi.Strip(m.View().Content), identity)
				require.Equal(t, row, sessions.Directory["remote"])
				require.Equal(t, identity, m.filtered[0].item.name)
				require.Equal(t, identity, m.filtered[0].item.searchName)
				m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				require.Equal(t, row, m.ChosenSession())
				require.Equal(t, identity, m.Chosen())
			}
		})
	}
}

func TestSSHDisplayMalformedIdentity(t *testing.T) {
	for _, identity := range []string{
		"ssh/host", "ssh/host/extra/path", "ssh/host/", "ssh//work",
		"ssh/host/%ZZ", "ssh/host/%2f", "ssh/%FF/work", "not-an-identity",
	} {
		t.Run(identity, func(t *testing.T) {
			row := model.SeshSession{Src: "ssh", Name: identity}
			m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{"remote": row}},
				testOptionsWith(func(o *Options) { o.ShowIcons = true }))
			require.Equal(t, "> ⚡ "+identity, iconRow(t, m, identity))
			m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			require.Equal(t, row, m.ChosenSession())
		})
	}
}

func TestSSHDisplayControlsStayOnOneRow(t *testing.T) {
	for _, test := range []struct{ session, label string }{
		{"line\nbreak\r\ttab", "line�break��tab"},
		{"escape\x1b[31m\a\x00\x7f\u0085", "escape�[31m����"},
	} {
		identity, err := model.EncodeSSHIdentity("host", test.session)
		require.NoError(t, err)
		row := model.SeshSession{Src: "ssh", Name: identity}
		m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{"remote": row}}, testOptions())
		require.Equal(t, "> host — "+test.label, iconRow(t, m, test.label))
		require.Len(t, strings.Split(m.View().Content, "\n"), m.height)
		m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		require.Equal(t, row, m.ChosenSession())
	}
	const malformed = "ssh/host/escape\x1b[31m"
	m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{
		"remote": {Src: "ssh", Name: malformed},
	}}, testOptions())
	require.Equal(t, "> ssh/host/escape�[31m", iconRow(t, m, "escape�[31m"))
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, malformed, m.ChosenSession().Name)
}

func TestSSHDisplaySameNamesRetainIdentity(t *testing.T) {
	sessions := model.SeshSessions{OrderedIndex: []string{"one", "two"}, Directory: model.SeshSessionMap{
		"one": {Src: "ssh", Name: "ssh/server1/production"},
		"two": {Src: "ssh", Name: "ssh/server2/production"},
	}}
	for i, key := range sessions.OrderedIndex {
		m := loadSSHModel(sessions, testOptionsWith(func(o *Options) { o.ShowIcons = true }))
		m, _ = typeFilter(m, "ssh")
		require.Contains(t, iconRow(t, m, "server1 — production"), "⚡ server1 — production")
		require.Contains(t, iconRow(t, m, "server2 — production"), "⚡ server2 — production")
		match := lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2)).Bold(true)
		require.Contains(t, m.View().Content, match.Render("⚡ ")+"server1 — production")
		require.Contains(t, m.View().Content, match.Render("⚡ ")+"server2 — production")
		if i > 0 {
			m = press(m, tea.KeyPressMsg{Code: tea.KeyDown})
		}
		label := []string{"server1 — production", "server2 — production"}[i]
		require.True(t, strings.HasPrefix(iconRow(t, m, label), "> ⚡ "))
		m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		require.Equal(t, sessions.Directory[key], m.ChosenSession())
	}
}

func TestSSHDisplayFuzzyHighlightsUseCanonicalOffsets(t *testing.T) {
	identity, err := model.EncodeSSHIdentity("host", "deploy/blue café%done")
	require.NoError(t, err)
	match := lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2)).Bold(true)
	session := "deploy/blue café%done"
	for _, test := range []struct{ query, highlighted string }{
		{"ssh", match.Render("h") + "ost — " + session},
		{"host/", match.Render("host") + " " + match.Render("—") + " " + session},
		{"deploy", "host — " + match.Render("deploy") + "/blue café%done"},
		{"%2F", "host — deploy" + match.Render("/") + "blue café%done"},
		{"%20", "host — deploy" + match.Render("/") + "blue" + match.Render(" ") + "café%done"},
		{"caf%C3%A9", "host — deploy/blue " + match.Render("café") + "%done"},
		{"%25", "host — deploy" + match.Render("/") + "blue café" + match.Render("%") + "done"},
	} {
		for _, separatorAware := range []bool{false, true} {
			t.Run(test.query+"/"+map[bool]string{false: "literal", true: "separators"}[separatorAware], func(t *testing.T) {
				m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{
					"remote": {Src: "ssh", Name: identity},
				}}, testOptionsWith(func(o *Options) { o.ShowIcons, o.SeparatorAware = true, separatorAware }))
				m, _ = typeFilter(m, test.query)
				require.Equal(t, []string{identity}, filteredNames(m))
				row := strings.Split(m.View().Content, "\n")[headerLines]
				tag := m.iconCell(m.filtered[0].item)
				if test.query == "ssh" {
					tag = match.Render(ansi.Strip(tag))
				}
				require.Equal(t, match.Render("> ")+tag+test.highlighted, row)
			})
		}
	}
}

func TestSSHDisplayPrefixOnlySourceHighlight(t *testing.T) {
	const identity = "ssh/myinventa/deploy"
	row := model.SeshSession{Src: "ssh", Name: identity}
	m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{"remote": row}},
		testOptionsWith(func(o *Options) { o.ShowIcons = true }))
	m, _ = typeFilter(m, "ssh")
	require.Equal(t, []int{0, 1, 2}, m.filtered[0].matchedIndexes)
	match := lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2)).Bold(true)
	rendered := strings.Split(m.View().Content, "\n")[headerLines]
	require.Equal(t, match.Render("> ")+match.Render("⚡ ")+"myinventa — deploy", rendered)
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, row, m.ChosenSession())
}

func TestSSHDisplaySourceMatchHighlighting(t *testing.T) {
	const identity = "ssh/myinventa/deploy"
	match := lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2)).Bold(true)
	for _, test := range []struct {
		query, label   string
		sourceMatch    bool
		separatorAware bool
	}{
		{query: "ssh", label: "myinventa — deploy", sourceMatch: true},
		{query: "ssh/", label: "myinventa — deploy", sourceMatch: true},
		{query: "myinventa", label: match.Render("myinventa") + " — deploy"},
		{query: "myinv", label: match.Render("myinv") + "enta — deploy"},
		{query: "deploy", label: "myinventa — " + match.Render("deploy")},
		{query: "depl", label: "myinventa — " + match.Render("depl") + "oy"},
		{query: "ssh/myinventa", label: match.Render("myinventa") + " — deploy", sourceMatch: true},
		{query: "ssh/deploy", label: "myinventa — " + match.Render("deploy"), sourceMatch: true},
		{query: "ssh myinventa", label: match.Render("myinventa") + " — deploy", sourceMatch: true, separatorAware: true},
	} {
		for _, icons := range []struct {
			name, icon, sourceTag string
			show                  bool
		}{
			{name: "default", sourceTag: "⚡ ", show: true},
			{name: "override", icon: lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(5)).Render("R"), sourceTag: "R  ", show: true},
			{name: "disabled", icon: "R"},
		} {
			t.Run(test.query+"/"+icons.name, func(t *testing.T) {
				row := model.SeshSession{Src: "ssh", Name: identity, Icon: icons.icon}
				m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{"remote": row}},
					testOptionsWith(func(o *Options) {
						o.ShowIcons, o.SeparatorAware = icons.show, test.separatorAware
						o.Icon = func(s model.SeshSession) string { return s.Icon }
					}))
				searchName := m.filtered[0].item.searchName
				m, _ = typeFilter(m, test.query)
				require.Equal(t, []string{identity}, filteredNames(m))
				label, tag := test.label, ""
				width := 2 + lipgloss.Width("myinventa — deploy")
				if icons.show {
					tag = m.iconCell(m.filtered[0].item)
					width += 3
					if test.sourceMatch {
						tag = match.Render(icons.sourceTag)
					}
				} else if test.sourceMatch {
					label = strings.Replace(label, " — ", " "+match.Render("—")+" ", 1)
				}
				rendered := strings.Split(m.View().Content, "\n")[headerLines]
				require.Equal(t, match.Render("> ")+tag+label, rendered)
				require.Equal(t, width, lipgloss.Width(rendered))
				require.Equal(t, searchName, m.filtered[0].item.searchName)
				require.Equal(t, identity, m.filtered[0].item.name)
				m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				require.Equal(t, row, m.ChosenSession())
			})
		}
	}
}

func TestSSHDisplaySourceMatchWithoutIconsUsesSeparator(t *testing.T) {
	identity, err := model.EncodeSSHIdentity("café—prod", "東京/deploy")
	require.NoError(t, err)
	m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{
		"remote": {Src: "ssh", Name: identity},
	}}, testOptions())
	m, _ = typeFilter(m, "ssh")
	match := lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2)).Bold(true)
	rendered := strings.Split(m.View().Content, "\n")[headerLines]
	require.Equal(t, match.Render("> ")+"café—prod "+match.Render("—")+" 東京/deploy", rendered)
	require.Equal(t, lipgloss.Width("> café—prod — 東京/deploy"), lipgloss.Width(rendered))
}

func TestSSHDisplaySourceMatchKeepsCanonicalPreviewIdentity(t *testing.T) {
	sessions := model.SeshSessions{OrderedIndex: []string{"one", "two"}, Directory: model.SeshSessionMap{
		"one": {Src: "ssh", Name: "ssh/server1/production"},
		"two": {Src: "ssh", Name: "ssh/server2/production"},
	}}
	var asked []string
	m := loadedSourcePreview(t, sessions, testPreviewFunc(&asked))
	m.showIcons = true
	seq := m.previewSeq
	m, cmd := typeFilter(m, "ssh")
	require.Nil(t, previewFetch(cmd), "styling a source match must not refetch the preview")
	require.Equal(t, seq, m.previewSeq)
	match := lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2)).Bold(true)
	for i, key := range sessions.OrderedIndex {
		if i > 0 {
			changed, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			m = loadPreview(t, changed.(Model), cmd)
		}
		row := sessions.Directory[key]
		target := previewTarget{src: "ssh", name: row.Name}
		require.Equal(t, target, m.previewTarget)
		require.True(t, m.previewMatches(target))
		require.False(t, m.previewMatches(previewTarget{src: "ssh", name: "server1 — production"}))
		require.Contains(t, m.previewCache, target)
		require.Contains(t, m.View().Content, "preview of "+row.Name)
		require.Contains(t, m.View().Content, match.Render("⚡ ")+"server1 — production")
		require.Contains(t, m.View().Content, match.Render("⚡ ")+"server2 — production")
		chosen := press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		require.Equal(t, row, chosen.ChosenSession())
	}
	changed, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = loadPreview(t, changed.(Model), cmd)
	require.Equal(t, "preview of ssh/server1/production", m.previewContent)
	require.Equal(t, []string{"ssh/server1/production", "ssh/server2/production"}, asked)
	require.Len(t, m.previewCache, 2)
}

func TestSSHDisplayOtherSourcesUnchanged(t *testing.T) {
	const name = "ssh/myinventa/deploy"
	for _, src := range []string{"tmux", "tmux-pane", "config", "zoxide", "tmuxinator", "other"} {
		t.Run(src, func(t *testing.T) {
			m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"local"}, Directory: model.SeshSessionMap{
				"local": {Src: src, Name: name},
			}}, testOptionsWith(func(o *Options) { o.ShowIcons = true }))
			require.Equal(t, "> "+ansi.Strip(icon.Cell("", src, 1))+name, iconRow(t, m, name))
			require.NotContains(t, m.View().Content, "⚡")
			m, _ = typeFilter(m, "ssh")
			match := lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2)).Bold(true)
			row := strings.Split(m.View().Content, "\n")[headerLines]
			require.Equal(t, match.Render("> ")+icon.Cell("", src, 1)+match.Render("ssh")+"/myinventa/deploy", row)
		})
	}
}

func TestSSHDisplayRowAlignment(t *testing.T) {
	sessions := model.SeshSessions{OrderedIndex: []string{"local", "remote", "custom"}, Directory: model.SeshSessionMap{
		"local":  {Src: "tmux", Name: "local"},
		"remote": {Src: "ssh", Name: "ssh/host/deploy"},
		"custom": {Src: "config", Name: "notes", Icon: "📓"},
	}}
	for _, width := range []int{2, 3} {
		m := loadSSHModel(sessions, testOptionsWith(func(o *Options) {
			o.ShowIcons, o.IconWidth = true, width
			o.Icon = func(s model.SeshSession) string { return s.Icon }
		}))
		for _, label := range []string{"local", "host — deploy", "notes"} {
			row := iconRow(t, m, label)
			require.Equal(t, 2+width+1, lipgloss.Width(row[:strings.Index(row, label)]))
		}
		require.Equal(t, 1, strings.Count(iconRow(t, m, "host — deploy"), "⚡"))
		require.NotContains(t, iconRow(t, m, "notes"), "⚡")
	}
}

func TestSSHDisplayWindowBudgetUsesVisibleWidth(t *testing.T) {
	identity, err := model.EncodeSSHIdentity("host", "deploy/blue café")
	require.NoError(t, err)
	m := loadSSHModel(model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{
		"remote": {Src: "ssh", Name: identity, WindowNames: []string{"editor", "server", "logs"}},
	}}, testOptionsWith(func(o *Options) { o.ShowIcons, o.ShowWindows = true, true }))
	m.width = 42
	row := iconRow(t, m, "host — deploy/blue café")
	require.Contains(t, row, "café editor")
	require.Contains(t, row, "+2")
	require.LessOrEqual(t, lipgloss.Width(row), m.contentWidth())
}
