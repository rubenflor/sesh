package picker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
)

// Deliver the messages produced by a command, including nested Bubble Tea batches.
// Follow-up commands (blink timers and preview debounce) remain owned by the caller.
func deliverCommand(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			m = deliverCommand(m, child)
		}
		return m
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func fzfModel(sessions model.SeshSessions, opts Options) Model {
	opts.Keymap = "fzf"
	m := New(testFetchFunc(sessions), opts)
	m = deliverCommand(m, m.Init())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	return next.(Model)
}

func TestFZFDirectSourceShortcuts(t *testing.T) {
	for _, tc := range []struct {
		key          rune
		source, name string
	}{
		{'t', "TMUX", "development"},
		{'g', "CONFIG", "configured"},
		{'x', "ZOXIDE", "~/projects"},
		{'a', "ALL", "ssh/myinventa/myinventa"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			m := fzfModel(sshPickerSessions(), testOptions())
			m = press(m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
			m = press(m, tea.KeyPressMsg{Code: tc.key, Mod: tea.ModCtrl})
			require.Contains(t, ansi.Strip(m.View().Content), "Source: "+tc.source)
			m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			require.Equal(t, tc.name, m.ChosenSession().Name)
		})
	}
}

func TestFZFNavigationAndActiveQuery(t *testing.T) {
	m := fzfModel(sshPickerSessions(), testOptions())
	m, _ = typeFilter(m, "de")
	m = press(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	view := ansi.Strip(m.View().Content)
	require.Contains(t, view, "> de")
	require.Contains(t, view, "development")
	require.NotContains(t, view, "myinventa — deploy")
	m = press(m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "ssh/myinventa/deploy", m.ChosenSession().Name)
	require.Empty(t, m.ChosenSession().Path)
}

func TestFZFRemovalBindingsKeepConfirmationAndSourceGuards(t *testing.T) {
	var killed, removed []string
	opts := testOptionsWith(func(o *Options) {
		o.Kill = func(id, generation string) error {
			killed = append(killed, id+"@"+generation)
			return nil
		}
		o.Remove = func(path string) error { removed = append(removed, path); return nil }
	})
	m := fzfModel(sshPickerSessions(), opts)
	m = press(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	require.NotContains(t, ansi.Strip(m.View().Content), "kill this tmux session")
	require.Empty(t, killed, "SSH must never reach the kill backend")
	m = press(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = press(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	require.Contains(t, ansi.Strip(m.View().Content), "kill this tmux session")
	require.Empty(t, killed, "opening the dialog is not confirmation")
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Empty(t, killed)
	m = press(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = deliverCommand(next.(Model), cmd)
	require.Equal(t, []string{"$1@" + testServerGeneration}, killed)
	m = press(m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Contains(t, ansi.Strip(m.View().Content), "Source: ZOXIDE")
	require.Empty(t, removed)
	m = press(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	require.NotContains(t, ansi.Strip(m.View().Content), "remove this directory from zoxide")
	m = press(m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	require.Contains(t, ansi.Strip(m.View().Content), "remove this directory from zoxide")
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	deliverCommand(next.(Model), cmd)
	require.Equal(t, []string{"/projects"}, removed)
}

func TestFZFPageNavigation(t *testing.T) {
	sessions := model.SeshSessions{Directory: model.SeshSessionMap{}}
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("item-%02d", i)
		sessions.OrderedIndex = append(sessions.OrderedIndex, name)
		sessions.Directory[name] = model.SeshSession{Src: "config", Name: name}
	}
	m := fzfModel(sessions, testOptions())
	m = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "item-10", m.ChosenSession().Name)
}

func TestFZFLocalKillRetainsBridgeAndGenerationGuards(t *testing.T) {
	for _, live := range []*model.TmuxSession{
		{ID: "$1", ServerGeneration: testServerGeneration, SSHBridgeIdentity: "ssh/host/work"},
		{ID: "$1", ServerGeneration: "replacement-generation"},
	} {
		t.Run(live.ServerGeneration+live.SSHBridgeIdentity, func(t *testing.T) {
			tm := tmux.NewMockTmux(t)
			tm.EXPECT().ListSessions().Return([]*model.TmuxSession{live}, nil).Once()
			p := &RealPicker{tmux: tm}
			m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) { o.Kill = p.killSession }))
			m = press(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
			m = press(m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
			next, cmd := m.Update(tea.KeyPressMsg{Code: 'y'})
			m = deliverCommand(next.(Model), cmd)
			require.Contains(t, ansi.Strip(m.View().Content), "local tmux session")
			m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			require.Equal(t, "development", m.ChosenSession().Name, "failed removal retains the row")
		})
	}
}

func TestFZFKeyHintsFitWithoutBottomLayout(t *testing.T) {
	m := fzfModel(sshPickerSessions(), testOptions())
	for _, width := range []int{60, 18} {
		m = press(m, tea.WindowSizeMsg{Width: width, Height: 12})
		lines := strings.Split(m.View().Content, "\n")
		require.Len(t, lines, 12)
		require.Contains(t, ansi.Strip(lines[1]), "Source: ALL")
		require.LessOrEqual(t, lipgloss.Width(lines[1]), width, "fzf key hints must not wrap into the list")
	}
}

func TestFZFKeyHintsWithPreviewFitBothInputLayouts(t *testing.T) {
	for _, position := range []string{"top", "bottom"} {
		t.Run(position, func(t *testing.T) {
			m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
				o.InputPosition, o.Preview, o.ShowIcons = position, true, true
				o.PreviewFunc = func(context.Context, model.SeshSession) (string, error) {
					return "\x1b[31mREMOTE_TOP\x1b[0m\nREMOTE_SECOND", nil
				}
			}))
			m = press(m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			next, debounce := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			m = next.(Model)
			next, capture := m.Update(debounce())
			m = deliverCommand(next.(Model), capture)
			for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 12}, {Width: 100, Height: 8}, {Width: 80, Height: 6}} {
				m = press(m, size)
				frame := m.View().Content
				lines := strings.Split(frame, "\n")
				require.Len(t, lines, size.Height, "hint must not wrap into extra rows")
				for _, line := range lines {
					require.LessOrEqual(t, lipgloss.Width(line), size.Width)
				}
				if size.Width < 100 {
					require.NotContains(t, frame, "REMOTE_TOP", "narrow terminals hide the preview")
					continue
				}
				firstRow, dividerRows := 2, size.Height
				if position == "bottom" {
					firstRow, dividerRows = 0, size.Height-2
				}
				row := ansi.Strip(lines[firstRow])
				require.Contains(t, row, "⚡ myinventa — myinventa")
				require.Contains(t, row, "REMOTE_TOP", "list and preview start on the same row")
				require.Equal(t, dividerRows, strings.Count(frame, "│"), "divider spans only its intended area")
			}
		})
	}
}
