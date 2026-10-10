package picker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/joshmedeski/sesh/v2/model"
)

// DirectoryFetchFunc discovers paths asynchronously under the invocation context.
type DirectoryFetchFunc func(context.Context) (model.SeshSessions, error)

type directoriesLoadedMsg struct {
	seq      int
	sessions model.SeshSessions
	err      error
}

func (p *RealPicker) findDirectories(ctx context.Context) (model.SeshSessions, error) {
	if err := ctx.Err(); err != nil {
		return model.SeshSessions{}, err
	}
	root, err := p.home.ExpandPath("~")
	if err != nil {
		return model.SeshSessions{}, err
	}
	if !filepath.IsAbs(root) {
		return model.SeshSessions{}, fmt.Errorf("directory search requires an absolute home path")
	}
	result, err := p.shell.CmdCaptureRaw(ctx, "fd", 4*1024*1024, 4*1024,
		"-H", "-d", "2", "-t", "d", "-E", ".Trash", "--absolute-path", "--print0", "--color", "never", "--", ".", root)
	if err != nil {
		return model.SeshSessions{}, fmt.Errorf("directory search requires fd: %w", err)
	}
	sessions := model.SeshSessions{Directory: model.SeshSessionMap{}}
	if len(result.Stdout) == 0 {
		return sessions, nil
	}
	if result.Stdout[len(result.Stdout)-1] != 0 {
		return model.SeshSessions{}, fmt.Errorf("incomplete fd directory output")
	}
	for _, path := range strings.Split(string(result.Stdout[:len(result.Stdout)-1]), "\x00") {
		if !filepath.IsAbs(path) {
			return model.SeshSessions{}, fmt.Errorf("fd returned a non-absolute directory path")
		}
		path = filepath.Clean(path)
		if _, exists := sessions.Directory[path]; exists {
			continue
		}
		sessions.OrderedIndex = append(sessions.OrderedIndex, path)
		sessions.Directory[path] = model.SeshSession{Src: "dir", Name: path, Path: path}
	}
	return sessions, nil
}

func (m *Model) cancelDirectories() {
	m.directorySeq++
	if m.directoryCancel != nil {
		m.directoryCancel()
		m.directoryCancel = nil
	}
	m.directoryLoading = false
}

func (m Model) startDirectorySearch() (tea.Model, tea.Cmd) {
	m.cancelDirectories()
	m.sourceFilter = "dir"
	m.directoryItems = nil
	m.applyFilter()
	m.cursor, m.offset = 0, 0
	m.aliasSeq++
	m.schedulePreview()
	if m.findDirectories == nil {
		m.status = "Directory search requires fd"
		return m, nil
	}
	ctx, cancel := context.WithCancel(m.previewCtx)
	m.directoryCancel = cancel
	m.directoryLoading = true
	seq, fetch := m.directorySeq, m.findDirectories
	return m, func() tea.Msg {
		defer cancel()
		sessions, err := fetch(ctx)
		return directoriesLoadedMsg{seq: seq, sessions: sessions, err: err}
	}
}

func (m Model) listLoading() bool {
	if m.sourceFilter == "dir" {
		return m.directoryLoading
	}
	return m.loading
}

func (m Model) activeItems() sessionItems {
	if m.sourceFilter == "dir" {
		return m.directoryItems
	}
	return m.allItems
}
