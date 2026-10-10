package picker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/icon"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/ls"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/pathwrap"
	"github.com/joshmedeski/sesh/v2/previewer"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
)

func TestFZFDirectoryDiscoveryRealFDAndSelection(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd is not installed")
	}
	root := filepath.Join(t.TempDir(), "home with spaces")
	selected := filepath.Join(root, ".hidden", "literal $SESH_FD_PATH")
	for _, path := range []string{selected, filepath.Join(root, ".Trash", "excluded"), filepath.Join(root, "one", "two", "too-deep")} {
		require.NoError(t, os.MkdirAll(path, 0700))
	}
	selected, err := filepath.EvalSymlinks(selected)
	require.NoError(t, err)
	h := home.NewMockHome(t)
	h.EXPECT().ExpandPath("~").Return(root, nil).Once()
	p := &RealPicker{home: h, shell: shell.NewShell(execwrap.NewExec(), h)}
	m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
		o.FindDirectories = p.findDirectories
	}))
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	m = next.(Model)
	require.True(t, m.Loading(), "discovery must be deferred, not block Update")
	m = deliverCommand(m, cmd)
	require.False(t, m.Loading())
	view := ansi.Strip(m.View().Content)
	require.Contains(t, view, selected)
	require.NotContains(t, view, "excluded")
	require.NotContains(t, view, "too-deep")
	m, _ = typeFilter(m, "literal")
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "dir", m.ChosenSession().Src)
	require.Equal(t, selected, m.ChosenSession().Name)
	require.Equal(t, selected, m.ChosenSession().Path)
}

func TestFZFDirectorySearchCancellationAndNormalListRestoration(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 't', Mod: tea.ModCtrl},
		{Code: 's', Mod: tea.ModCtrl},
		{Code: tea.KeyEscape},
	} {
		t.Run(key.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			started := make(chan struct{})
			m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
				o.Context = ctx
				o.FindDirectories = func(ctx context.Context) (model.SeshSessions, error) {
					close(started)
					<-ctx.Done()
					return model.SeshSessions{}, ctx.Err()
				}
			}))
			next, cmd := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
			m = next.(Model)
			done := make(chan tea.Msg, 1)
			go func() { done <- cmd() }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("directory command did not start")
			}
			m = press(m, key)
			select {
			case msg := <-done:
				m = press(m, msg)
			case <-time.After(2 * time.Second):
				t.Fatal("leaving directory search did not cancel its command")
			}
			require.NotContains(t, ansi.Strip(m.View().Content), "context canceled", "obsolete results must be ignored")
			if key.Code == tea.KeyEscape {
				require.True(t, m.Quit())
			} else {
				m = press(m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
				m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				require.Equal(t, "ssh/myinventa/myinventa", m.ChosenSession().Name)
			}
		})
	}
}

func TestFZFDirectorySearchCtrlSDuringInitialLoading(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	inventoryStarted, inventoryReady := make(chan struct{}), make(chan struct{})
	var releaseInventory sync.Once
	directoryStarted, directoryCanceled := make(chan struct{}), make(chan struct{})
	directoryReady := make(chan struct{})
	var releaseDirectory sync.Once
	m := New(func() (model.SeshSessions, error) {
		close(inventoryStarted)
		select {
		case <-inventoryReady:
			return sshPickerSessions(), nil
		case <-ctx.Done():
			return model.SeshSessions{}, ctx.Err()
		}
	}, testOptionsWith(func(o *Options) {
		o.Context, o.Keymap, o.Query = ctx, "fzf", "myinventa"
		o.FindDirectories = func(ctx context.Context) (model.SeshSessions, error) {
			close(directoryStarted)
			<-ctx.Done()
			close(directoryCanceled)
			<-directoryReady
			const obsolete = "/tmp/myinventa-OBSOLETE_FD"
			return model.SeshSessions{OrderedIndex: []string{obsolete}, Directory: model.SeshSessionMap{
				obsolete: {Src: "dir", Name: obsolete, Path: obsolete},
			}}, nil
		}
	}))
	m = press(m, tea.WindowSizeMsg{Width: 120, Height: 12})
	inventoryMessages, inventoryFinished := make(chan tea.Msg, 4), make(chan struct{})
	var runCommand func(tea.Cmd)
	runCommand = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				runCommand(child)
			}
			return
		}
		inventoryMessages <- msg
	}
	init := m.Init()
	go func() { defer close(inventoryFinished); runCommand(init) }()
	t.Cleanup(func() {
		cancel()
		releaseInventory.Do(func() { close(inventoryReady) })
		select {
		case <-inventoryFinished:
		case <-time.After(3 * time.Second):
			t.Error("initial inventory cleanup timed out")
		}
	})
	select {
	case <-inventoryStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("initial inventory did not start")
	}
	next, search := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	m = next.(Model)
	directoryMessage, directoryFinished := make(chan tea.Msg, 1), make(chan struct{})
	go func() { defer close(directoryFinished); directoryMessage <- search() }()
	t.Cleanup(func() {
		cancel()
		releaseDirectory.Do(func() { close(directoryReady) })
		select {
		case <-directoryFinished:
		case <-time.After(3 * time.Second):
			t.Error("directory discovery cleanup timed out")
		}
	})
	select {
	case <-directoryStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("directory discovery did not start")
	}
	m = press(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	require.Contains(t, ansi.Strip(m.View().Content), "Source: ALL")
	require.Contains(t, ansi.Strip(m.View().Content), "> myinventa", "source switching keeps the query")
	require.True(t, m.Loading(), "Ctrl-S must not cancel the initial inventory")
	select {
	case <-directoryCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl-S did not cancel directory discovery during initial loading")
	}
	releaseInventory.Do(func() { close(inventoryReady) })
	for m.Loading() {
		select {
		case msg := <-inventoryMessages:
			m = press(m, msg)
		case <-time.After(3 * time.Second):
			t.Fatal("initial inventory did not complete")
		}
	}
	releaseDirectory.Do(func() { close(directoryReady) })
	select {
	case msg := <-directoryMessage:
		m = press(m, msg)
	case <-time.After(3 * time.Second):
		t.Fatal("obsolete directory result did not complete")
	}
	require.NotContains(t, ansi.Strip(m.View().Content), "OBSOLETE_FD")
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "ssh/myinventa/myinventa", m.ChosenSession().Name)
}

func TestFZFDirectorySearchMissingFDIsNonfatal(t *testing.T) {
	h := home.NewMockHome(t)
	h.EXPECT().ExpandPath("~").Return(t.TempDir(), nil).Once()
	executor := execwrap.NewMockExec(t)
	executor.EXPECT().LookPath("fd").Return("", exec.ErrNotFound).Once()
	p := &RealPicker{home: h, shell: shell.NewShell(executor, h)}
	m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) { o.FindDirectories = p.findDirectories }))
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	m = deliverCommand(next.(Model), cmd)
	require.False(t, m.Loading())
	require.NoError(t, m.LoadErr())
	require.False(t, m.Quit())
	require.Contains(t, ansi.Strip(m.View().Content), "requires fd")
	m = press(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "development", m.ChosenSession().Name)
}

func TestFZFDirectorySearchLoadingAndAbsoluteQuery(t *testing.T) {
	const path = "/tmp/picker-directory"
	m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
		o.FindDirectories = func(context.Context) (model.SeshSessions, error) {
			return model.SeshSessions{OrderedIndex: []string{path}, Directory: model.SeshSessionMap{
				path: {Src: "dir", Name: path, Path: path},
			}}, nil
		}
	}))
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	m = next.(Model)
	require.Contains(t, ansi.Strip(m.View().Content), "Loading directories...")
	m = deliverCommand(m, cmd)
	m, _ = typeFilter(m, path)
	view := ansi.Strip(m.View().Content)
	require.NotContains(t, view, "No aliases configured")
	require.Contains(t, view, "> "+path)
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, path, m.ChosenSession().Path)
}

func TestFZFDirectoryControlCharactersAreDisplayOnly(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd is not installed")
	}
	root := t.TempDir()
	path := filepath.Join(root, "line\nbreak\x1b[31m")
	require.NoError(t, os.Mkdir(path, 0700))
	path, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	h := home.NewMockHome(t)
	h.EXPECT().ExpandPath("~").Return(root, nil).Once()
	p := &RealPicker{home: h, shell: shell.NewShell(execwrap.NewExec(), h)}
	m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) { o.FindDirectories = p.findDirectories }))
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	m = deliverCommand(next.(Model), cmd)
	view := ansi.Strip(m.View().Content)
	require.Len(t, strings.Split(view, "\n"), 12)
	require.Contains(t, view, "line�break�[31m")
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, path, m.ChosenSession().Path)
	require.Equal(t, path, m.ChosenSession().Name)
}

func TestFZFDirectoryPreviewUsesLiteralSelectedPath(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	literal := filepath.Join(root, "literal $SESH_T56_PREVIEW_COMPONENT")
	expanded := filepath.Join(root, "literal expanded directory")
	for _, path := range []string{literal, expanded} {
		require.NoError(t, os.Mkdir(path, 0700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(literal, "REQUESTED_DIRECTORY_CONTENT"), nil, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(expanded, "WRONG_DIRECTORY_CONTENT"), nil, 0600))
	t.Setenv("SESH_T56_PREVIEW_COMPONENT", "expanded directory")
	osw := oswrap.NewMockOs(t)
	osw.EXPECT().UserHomeDir().Return(root, nil).Maybe()
	osw.EXPECT().ExpandEnv(mock.Anything).RunAndReturn(os.ExpandEnv).Maybe()
	h := home.NewHome(osw)
	sh := shell.NewShell(execwrap.NewExec(), h)
	tm := tmux.NewMockTmux(t)
	tm.EXPECT().ListSessions().Return(nil, nil).Maybe()
	zo := zoxide.NewMockZoxide(t)
	config := model.Config{}
	li := lister.NewLister(config, h, tm, zo, nil, nil, nil, nil)
	pr := previewer.NewPreviewer(li, tm, icon.NewIcon(config),
		dir.NewDir(oswrap.NewOs(), nil, pathwrap.NewPath()), h, ls.NewLs(config, sh), config, sh)
	p := &RealPicker{home: h, shell: sh, previewer: pr}
	m := fzfModel(model.SeshSessions{}, testOptionsWith(func(o *Options) {
		o.InputPosition, o.Query, o.Preview = "bottom", literal, true
		o.Context, o.FindDirectories, o.PreviewFunc = t.Context(), p.findDirectories, p.previewSession
	}))
	next, search := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	m = next.(Model)
	next, debounce := m.Update(search())
	m = next.(Model)
	require.NotNil(t, debounce, "the selected fd directory must schedule its preview")
	next, capture := m.Update(debounce())
	m = deliverCommand(next.(Model), capture)
	frame := ansi.Strip(m.View().Content)
	require.Contains(t, frame, "REQUESTED_DIRECTORY_CONTENT")
	require.NotContains(t, frame, "WRONG_DIRECTORY_CONTENT")
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, literal, m.ChosenSession().Name)
	require.Equal(t, literal, m.ChosenSession().Path)
}
