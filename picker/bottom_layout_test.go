package picker

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/model"
)

func TestBottomInputLayoutWithSSHPreviewAndEditing(t *testing.T) {
	m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
		o.InputPosition = "bottom"
		o.ShowIcons = true
		o.PreviewFunc = func(_ context.Context, session model.SeshSession) (string, error) {
			require.Equal(t, "ssh/myinventa/myinventa", session.Name)
			return "REMOTE_TOP\nREMOTE_SECOND", nil
		}
	}))
	next, debounce := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = next.(Model)
	require.NotNil(t, debounce)
	next, capture := m.Update(debounce())
	m = deliverCommand(next.(Model), capture)
	view := m.View()
	lines := strings.Split(ansi.Strip(view.Content), "\n")
	require.Len(t, lines, 12)
	require.Contains(t, lines[0], "⚡ myinventa — myinventa")
	require.Contains(t, lines[0], "REMOTE_TOP", "preview starts at the top, not below input padding")
	require.Contains(t, lines[10], "Source: ALL")
	require.Contains(t, lines[11], "> Filter sessions...")
	require.Nil(t, view.Cursor, "retain the virtual text cursor; do not import upstream #497")
	m, _ = typeFilter(m, "myinventx")
	m = press(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = press(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	lines = strings.Split(ansi.Strip(m.View().Content), "\n")
	require.Contains(t, lines[11], "> myinventa")
	m = press(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	lines = strings.Split(ansi.Strip(m.View().Content), "\n")
	require.Len(t, lines, 12, "status must not move the input")
	require.Contains(t, lines[11], "> myinventa")
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "ssh/myinventa/myinventa", m.ChosenSession().Name)
}

func TestBottomInputResizeKeepsSelectionAndBoundsTinyFrames(t *testing.T) {
	sessions := model.SeshSessions{Directory: model.SeshSessionMap{}}
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("row-%02d-%s", i, strings.Repeat("界", 40))
		sessions.OrderedIndex = append(sessions.OrderedIndex, name)
		sessions.Directory[name] = model.SeshSession{Src: "config", Name: name}
	}
	m := fzfModel(sessions, testOptionsWith(func(o *Options) { o.InputPosition = "bottom" }))
	for i := 0; i < 9; i++ {
		m = press(m, tea.KeyPressMsg{Code: tea.KeyTab})
	}
	m = press(m, tea.WindowSizeMsg{Width: 18, Height: 4})
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	require.Len(t, lines, 4)
	require.Contains(t, strings.Join(lines[:2], "\n"), "> row-09-", "shrinking the viewport keeps the highlight visible")
	for _, size := range []tea.WindowSizeMsg{{Width: 18, Height: 4}, {Width: 8, Height: 2}, {Width: 1, Height: 1}, {Width: 120, Height: 12}} {
		m = press(m, size)
		lines := strings.Split(m.View().Content, "\n")
		require.Len(t, lines, size.Height)
		for _, line := range lines {
			require.LessOrEqual(t, lipgloss.Width(line), size.Width)
		}
	}
	m, _ = typeFilter(m, strings.Repeat("界a", 60))
	m = press(m, tea.WindowSizeMsg{Width: 18, Height: 4})
	input := strings.Split(m.View().Content, "\n")[3]
	require.LessOrEqual(t, lipgloss.Width(input), 18)
	assertVisibleInputCursor(t, input, 18, " ")
}

func assertVisibleInputCursor(t *testing.T, line string, width int, char string) {
	t.Helper()
	match := regexp.MustCompile(`\x1b\[(?:\d+;)*7(?:;\d+)*m([^\x1b]+)`).FindStringSubmatchIndex(line)
	require.NotNil(t, match, "reverse-video codes without a rendered character are not a visible cursor: %q", line)
	cell := line[match[2]:match[3]]
	require.Equal(t, char, cell, "cursor highlights the character at the logical editing position")
	x := lipgloss.Width(ansi.Strip(line[:match[2]]))
	require.GreaterOrEqual(t, x, 4, "cursor must follow the indentation and prompt")
	require.LessOrEqual(t, x+lipgloss.Width(cell), width, "cursor character must fit within terminal bounds")
}

func TestBottomInputResizePreservesVisibleEditingCursor(t *testing.T) {
	for _, query := range []string{strings.Repeat("0123456789", 4), strings.Repeat("界a", 20)} {
		t.Run(query[:3], func(t *testing.T) {
			m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) { o.InputPosition = "bottom" }))
			m, _ = typeFilter(m, query)
			for i := 0; i < 10; i++ {
				m = press(m, tea.KeyPressMsg{Code: tea.KeyLeft})
			}
			runes := []rune(query)
			for _, width := range []int{18, 120, 8, 18, 120} {
				m = press(m, tea.WindowSizeMsg{Width: width, Height: 4})
				lines := strings.Split(m.View().Content, "\n")
				require.Len(t, lines, 4)
				input := lines[3]
				require.LessOrEqual(t, lipgloss.Width(input), width)
				assertVisibleInputCursor(t, input, width, string(runes[30]))
				if width == 120 {
					require.Contains(t, ansi.Strip(input), "> "+query, "resizing preserves the complete query")
				}
			}
			m = press(m, tea.KeyPressMsg{Code: 'Z', Text: "Z"})
			input := strings.Split(m.View().Content, "\n")[3]
			require.Contains(t, ansi.Strip(input), "> "+string(runes[:30])+"Z"+string(runes[30:]), "logical cursor survives resize")
			m = press(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
			input = strings.Split(m.View().Content, "\n")[3]
			require.Contains(t, ansi.Strip(input), "> "+query)
			assertVisibleInputCursor(t, input, 120, string(runes[30]))
		})
	}
}

func TestBottomInputConfirmationStaysCenteredAndFitsSmallFrames(t *testing.T) {
	kills := 0
	m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
		o.InputPosition = "bottom"
		o.Kill = func(string, string) error { kills++; return nil }
	}))
	m = press(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = press(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	require.Len(t, lines, 12)
	require.Contains(t, lines[0], "development", "the centered dialog leaves the first list row visible")
	require.Contains(t, lines[11], "> Filter sessions...")
	for _, size := range []tea.WindowSizeMsg{{Width: 20, Height: 4}, {Width: 8, Height: 2}} {
		m = press(m, size)
		lines := strings.Split(m.View().Content, "\n")
		require.Len(t, lines, size.Height)
		for _, line := range lines {
			require.LessOrEqual(t, lipgloss.Width(line), size.Width)
		}
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliverCommand(next.(Model), cmd)
	require.Zero(t, kills, "a terminal too small to show the choices must not confirm blindly")
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = press(m, tea.WindowSizeMsg{Width: 120, Height: 12})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "development", m.ChosenSession().Name)
}
