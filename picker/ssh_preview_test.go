package picker

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/model"
)

func loadedSourcePreview(t *testing.T, sessions model.SeshSessions, preview PreviewFunc) Model {
	t.Helper()
	m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
		o.Preview, o.PreviewFunc = true, preview
	}))
	m.width, m.height = 120, 24
	loaded, cmd := m.Update(m.fetchSessions()())
	return loadPreview(t, loaded.(Model), cmd)
}

func TestSSHPreviewSourceTransition(t *testing.T) {
	sessions := sshPickerSessions()
	m := loadedSourcePreview(t, sessions, testPreviewFunc(&[]string{}))
	sshContent := m.previewContent
	require.Contains(t, m.View().Content, sshContent)
	m = press(m, ctrlS)
	require.Equal(t, "ssh", m.sourceFilter)

	changed, cmd := m.Update(ctrlS)
	m = changed.(Model)
	require.Equal(t, "tmux", m.sourceFilter)
	require.Equal(t, sessions.Directory["local1"], m.filtered[m.cursor].item.session)
	require.Equal(t, sshContent, m.previewContent, "retain the cache without displaying it under another row")
	require.NotContains(t, m.View().Content, sshContent)
	require.Contains(t, m.View().Content, "Loading preview...")
	require.Empty(t, m.ChosenSession(), "filter changes cannot select a connection")

	m = loadPreview(t, m, cmd)
	tmuxContent := m.previewContent
	require.NotEqual(t, sshContent, tmuxContent)
	require.Contains(t, m.View().Content, tmuxContent)
	require.NotContains(t, m.View().Content, sshContent)
	chosen, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, sessions.Directory["local1"], chosen.(Model).ChosenSession())

	for m.sourceFilter != "" {
		changed, cmd = m.Update(ctrlS)
		m = changed.(Model)
	}
	require.Equal(t, sessions.Directory["remote1"], m.filtered[m.cursor].item.session)
	require.NotContains(t, m.View().Content, tmuxContent, "All now highlights the SSH row")
	require.Contains(t, m.View().Content, "Loading preview...")
	m = loadPreview(t, m, cmd)
	require.Contains(t, m.View().Content, sshContent)
	m = press(m, ctrlS)
	require.Equal(t, "ssh", m.sourceFilter)
	require.Contains(t, m.View().Content, sshContent)
	chosen, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, sessions.Directory["remote1"], chosen.(Model).ChosenSession())
}

func TestSSHPreviewRapidSourceChangesRejectLateResults(t *testing.T) {
	for _, destination := range []string{"config", ""} {
		t.Run(destination, func(t *testing.T) {
			m := loadedSourcePreview(t, sshPickerSessions(), testPreviewFunc(&[]string{}))
			sshContent := m.previewContent
			m = press(m, ctrlS)
			changed, cmd := m.Update(ctrlS)
			m = changed.(Model)
			fetch := previewFetch(cmd)
			require.NotNil(t, fetch)
			started, held := m.Update(*fetch)
			m = started.(Model)
			require.NotNil(t, held)

			for m.sourceFilter != destination {
				changed, cmd = m.Update(ctrlS)
				m = changed.(Model)
			}
			if destination == "" {
				require.Nil(t, cmd, "returning to the cached SSH identity needs no fetch")
				require.Contains(t, m.View().Content, sshContent)
			} else {
				require.NotContains(t, m.View().Content, sshContent)
				require.Contains(t, m.View().Content, "Loading preview...")
				m = loadPreview(t, m, cmd)
			}
			current := m.previewContent
			late := held().(previewLoadedMsg)
			completed, _ := m.Update(late)
			m = completed.(Model)
			require.Equal(t, current, m.previewContent)
			require.Contains(t, m.View().Content, current)
			require.NotContains(t, m.View().Content, late.content)
			require.Empty(t, m.ChosenSession())
		})
	}
}

func TestSSHPreviewEmptySourceHidesPreviousContent(t *testing.T) {
	for _, previous := range []struct {
		name string
		err  error
	}{
		{name: "content"},
		{name: "error", err: errors.New("previous SSH error")},
	} {
		t.Run(previous.name, func(t *testing.T) {
			sessions := sshPickerSessions()
			calls := 0
			m := loadedSourcePreview(t, sessions, func(_ context.Context, row model.SeshSession) (string, error) {
				require.Equal(t, sessions.Directory["remote1"], row)
				calls++
				if calls == 1 && previous.err != nil {
					return "", previous.err
				}
				return "preview of " + row.Name, nil
			})
			old := m.previewContent
			if previous.err != nil {
				old = previous.err.Error()
			}
			require.Contains(t, m.View().Content, old)
			m, _ = typeFilter(m, "myinventa")
			m = press(m, ctrlS)
			changed, cmd := m.Update(ctrlS)
			m = changed.(Model)
			require.Equal(t, "tmux", m.sourceFilter)
			require.Equal(t, "myinventa", m.filterInput.Value())
			require.Empty(t, m.filtered)
			require.Nil(t, cmd, "no highlighted session schedules no preview")
			require.NotContains(t, m.View().Content, old)
			require.Contains(t, m.View().Content, "No preview")
			require.NotContains(t, m.View().Content, "Loading preview...")
			chosen, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.Empty(t, chosen.(Model).ChosenSession())

			for m.sourceFilter != "" {
				changed, cmd = m.Update(ctrlS)
				m = changed.(Model)
			}
			m = press(m, ctrlS)
			require.Equal(t, "ssh", m.sourceFilter)
			require.Equal(t, sessions.Directory["remote1"], m.filtered[m.cursor].item.session)
			require.NotContains(t, m.View().Content, old)
			require.NotContains(t, m.View().Content, "No preview")
			require.Contains(t, m.View().Content, "Loading preview...")
			m = loadPreview(t, m, cmd)
			require.Equal(t, previous.err, m.previewErr)
			require.Contains(t, m.View().Content, old, "returning to SSH restores its cached result")
			require.NotContains(t, m.View().Content, "Loading preview...")
			require.Equal(t, 1, calls)
		})
	}
}

func TestSSHPreviewQueryChangesDuringPendingFetch(t *testing.T) {
	sessions := sshPickerSessions()
	m := loadedSourcePreview(t, sessions, testPreviewFunc(&[]string{}))
	sshContent := m.previewContent
	m = press(m, ctrlS)
	changed, cmd := m.Update(ctrlS)
	m = changed.(Model)
	fetch := previewFetch(cmd)
	require.NotNil(t, fetch)
	started, held := m.Update(*fetch)
	m = started.(Model)
	require.NotNil(t, held)

	// The first key changes the target; subsequent keys reuse that pending fetch.
	m, cmd = typeFilter(m, "i")
	m, _ = typeFilter(m, "nfra")
	require.Equal(t, "infra", m.filterInput.Value())
	require.Equal(t, sessions.Directory["local2"], m.filtered[m.cursor].item.session)
	require.NotContains(t, m.View().Content, sshContent)
	require.Contains(t, m.View().Content, "Loading preview...")
	m = loadPreview(t, m, cmd)
	current := m.previewContent
	late := held().(previewLoadedMsg)
	completed, _ := m.Update(late)
	m = completed.(Model)
	require.Equal(t, current, m.previewContent)
	require.Contains(t, m.View().Content, current)
	require.NotContains(t, m.View().Content, late.content)
	chosen, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, sessions.Directory["local2"], chosen.(Model).ChosenSession())
}

func TestSSHPreviewErrorBelongsToHighlightedTarget(t *testing.T) {
	m := loadedSourcePreview(t, sshPickerSessions(), func(_ context.Context, row model.SeshSession) (string, error) {
		if row.Src == "ssh" {
			return "", errors.New("previous SSH error")
		}
		return "current tmux preview", nil
	})
	require.Contains(t, m.View().Content, "previous SSH error")
	m = press(m, ctrlS)
	require.Contains(t, m.View().Content, "previous SSH error", "the same SSH identity keeps its valid error")
	changed, cmd := m.Update(ctrlS)
	m = changed.(Model)
	require.Error(t, m.previewErr, "the cached error is retained")
	require.NotContains(t, m.View().Content, "previous SSH error")
	require.Contains(t, m.View().Content, "Loading preview...")
	m = loadPreview(t, m, cmd)
	require.NoError(t, m.previewErr)
	require.Contains(t, m.View().Content, "current tmux preview")
}

func TestSSHPreviewUnchangedIdentityKeepsCachedContent(t *testing.T) {
	row := sshPickerSessions().Directory["remote1"]
	sessions := model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{"remote": row}}
	var asked []string
	m := loadedSourcePreview(t, sessions, testPreviewFunc(&asked))
	content, seq := m.previewContent, m.previewSeq
	for _, source := range []string{"ssh", "", "ssh"} {
		changed, cmd := m.Update(ctrlS)
		m = changed.(Model)
		require.Equal(t, source, m.sourceFilter)
		require.Equal(t, seq, m.previewSeq)
		require.Nil(t, cmd)
		require.Contains(t, m.View().Content, content)
		require.NotContains(t, m.View().Content, "Loading preview...")
	}
	require.Equal(t, []string{row.Name}, asked)
}
