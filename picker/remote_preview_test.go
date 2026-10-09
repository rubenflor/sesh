package picker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/previewer"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
)

func TestSSHRemotePreviewCaptureAndShellBoundaries(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	require.NoError(t, err)
	binDir := t.TempDir()
	// Mock SSH at the shell seam, then parse its remote command with a real shell.
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "tmux"), []byte(`#!/bin/sh
if [ "$1" = -u ]; then shift; elif [ "$1" != capture-pane ]; then exit 9; fi
case "$1" in
 list-sessions)
  [ "$#" -eq 3 ] && [ "$2" = -F ] && [ "$3" = '#{session_id}' ] || exit 9
  printf '$1\n$42\n' ;;
 display-message)
  [ "$#" -eq 5 ] && [ "$2" = -p ] && [ "$3" = -t ] && [ "$5" = '#{session_name}x' ] || exit 9
  case "$4" in
   '$1:') printf 'otherx\n' ;;
   '$42:') printf '%sx\n' "$SSH_PREVIEW_STORED_NAME" ;;
   *) exit 9 ;;
  esac ;;
 capture-pane)
  [ "$#" -eq 4 ] && [ "$2" = -p ] && [ "$3" = -t ] && [ "$4" = '$42:' ] || exit 9
  printf '%s\0' "$@" ;;
 *) exit 9 ;;
esac
`), 0o700))
	for _, test := range []struct{ name, host, session, stored string }{
		{"plain", "host", "work", "work"},
		{"spaces", "host alias", "two words", "two words"},
		{"slashes", "my/host", "work/blue", "work/blue"},
		{"decode once", "host%alias", "literal%2F", "literal%2F"},
		{"Unicode", "höst", "café 東京", "café 東京"},
		{"quotes", `host'"alias`, `it's "ready"`, `it's "ready"`},
		{"substitution", "host", `$(touch "$SSH_PREVIEW_INJECTED")`, `$(touch "$SSH_PREVIEW_INJECTED")`},
		{"backticks", "host", "`touch \"$SSH_PREVIEW_INJECTED\"`", "`touch \"$SSH_PREVIEW_INJECTED\"`"},
		{"quote breakout", "host", `work'; touch "$SSH_PREVIEW_INJECTED"; #`, `work'; touch "$SSH_PREVIEW_INJECTED"; #`},
		{"semicolons", "host", "work;;", "work;;"},
		{"glob", "host", "work*[ab]?", "work*[ab]?"},
		{"ID shaped", "host", "$1", "$1"},
		{"leading dollar", "host", "$foo", "$foo"},
		{"colon", "host", "colon:name", "colon:name"},
		{"delimiters", "host", `::|,#{session_id}#}"`, `::|,#{session_id}#}"`},
		{"leading options", "-oProxyCommand=evil", "-target", "-target"},
		{"newline", "host", "work\nnext", "work\\nnext"},
		{"literal backslash", "host", `work\n`, `work\\n`},
		{"tab and control", "host", "work\t\x01", "work\\t\\001"},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, err := model.EncodeSSHIdentity(test.host, test.session)
			require.NoError(t, err)
			injected := filepath.Join(t.TempDir(), "injected")
			sh := shell.NewMockShell(t)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
				"-T", "-n", "-o", "BatchMode=yes", "--", test.host, mock.Anything).
				RunAndReturn(func(ctx context.Context, _ string, _, _ int, args ...string) (execwrap.CommandResult, error) {
					deadline, ok := ctx.Deadline()
					require.True(t, ok)
					require.Greater(t, time.Until(deadline), time.Duration(0))
					require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
					process := exec.CommandContext(ctx, shPath, "-c", args[6])
					process.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
						"SSH_PREVIEW_INJECTED="+injected, "SSH_PREVIEW_STORED_NAME="+test.stored)
					output, err := process.CombinedOutput()
					require.NoError(t, err, "%s", output)
					require.Equal(t, "capture-pane\x00-p\x00-t\x00$42:\x00", string(output))
					return execwrap.CommandResult{Stdout: []byte("REMOTE_PANE\n\n")}, nil
				}).Once()
			p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{test.host}}},
				shell: sh, tmux: tmux.NewMockTmux(t), previewer: previewer.NewMockPreviewer(t)}
			output, err := p.previewSession(t.Context(), model.SeshSession{Src: "ssh", Name: identity})
			require.NoError(t, err)
			require.Equal(t, "REMOTE_PANE\n\n", output)
			_, err = os.Stat(injected)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func remotePreviewCommandFor(name string) any {
	return mock.MatchedBy(func(command string) bool {
		return strings.HasPrefix(command, "/bin/sh -c 'wanted='\\''"+name+"'\\''\n")
	})
}

func TestSSHRemotePreviewLookupFailsClosed(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("$42", nil, 0600))
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tmux"), []byte(`#!/bin/sh
[ "$1" != -u ] || shift
case "$1" in
 list-sessions)
  if [ "$SSH_PREVIEW_FAIL" = list ]; then printf 'list failed\n' >&2; exit 2; fi
  printf '%s' "$SSH_PREVIEW_IDS" ;;
 display-message)
  if [ "$SSH_PREVIEW_FAIL" = name ]; then printf 'name read failed\n' >&2; exit 2; fi
  printf '%sx\n' "$SSH_PREVIEW_NAME" ;;
 capture-pane)
  : > "$SSH_PREVIEW_CAPTURED"
  printf 'capture failed\n' >&2
  exit 2 ;;
 *) exit 9 ;;
esac
`), 0700))
	for _, test := range []struct {
		label, ids, stored, fail, message string
		capture                           bool
	}{
		{label: "empty inventory", message: "not found"},
		{label: "missing name", ids: "$42\n", stored: "other", message: "not found"},
		{label: "prefix is not exact", ids: "$42\n", stored: "workspace", message: "not found"},
		{label: "substring is not exact", ids: "$42\n", stored: "before work after", message: "not found"},
		{label: "missing dollar", ids: "42\n", stored: "work", message: "invalid remote tmux session ID"},
		{label: "missing digits", ids: "$\n", stored: "work", message: "invalid remote tmux session ID"},
		{label: "nonnumeric ID", ids: "$42x\n", stored: "work", message: "invalid remote tmux session ID"},
		{label: "ID glob cannot expand", ids: "$4[2]\n", stored: "work", message: "invalid remote tmux session ID"},
		{label: "invalid record after match", ids: "$42\n$43;\n", stored: "work", message: "invalid remote tmux session ID"},
		{label: "ambiguous names", ids: "$42\n$43\n", stored: "work", message: "ambiguous"},
		{label: "failed inventory", fail: "list", message: "list failed"},
		{label: "failed name read", ids: "$42\n", fail: "name", message: "name read failed"},
		{label: "failed capture", ids: "$42\n", stored: "work", message: "capture failed", capture: true},
	} {
		t.Run(test.label, func(t *testing.T) {
			captured := filepath.Join(t.TempDir(), "captured")
			transport := shell.NewMockShell(t)
			transport.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
				"-T", "-n", "-o", "BatchMode=yes", "--", "host", remotePreviewCommandFor("work")).
				RunAndReturn(func(ctx context.Context, _ string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
					return shell.NewShell(execwrap.NewExec(), nil).CmdCaptureRaw(ctx, "/usr/bin/env", stdoutLimit, stderrLimit,
						"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SSH_PREVIEW_IDS="+test.ids,
						"SSH_PREVIEW_NAME="+test.stored, "SSH_PREVIEW_FAIL="+test.fail,
						"SSH_PREVIEW_CAPTURED="+captured, "/bin/sh", "-c", args[6])
				}).Once()
			p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}}, shell: transport}
			output, err := p.previewSession(t.Context(), model.SeshSession{Src: "ssh", Name: "ssh/host/work"})
			require.ErrorContains(t, err, test.message)
			require.Empty(t, output)
			_, err = os.Stat(captured)
			if test.capture {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, os.ErrNotExist, "unresolved or ambiguous names must never capture a pane")
			}
		})
	}
}

func TestSSHRemotePreviewRejectsUnconfiguredOrInvalidIdentity(t *testing.T) {
	for _, identity := range []string{
		"ssh/other/work", "ssh/host/work/extra", "ssh/host/%GG", "ssh/host/a%2fb",
		"ssh/host/work%00", "ssh/ho%00st/work",
	} {
		t.Run(identity, func(t *testing.T) {
			p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"host", "ho\x00st"}}},
				shell: shell.NewMockShell(t), tmux: tmux.NewMockTmux(t), previewer: previewer.NewMockPreviewer(t)}
			output, err := p.previewSession(t.Context(), model.SeshSession{Src: "ssh", Name: identity})
			require.Error(t, err)
			require.Empty(t, output)
		})
	}
}

func TestSSHRemotePreviewEmptyAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, stdout, stderr, message string
		status                        int
		err                           error
	}{
		{name: "empty"},
		{name: "blank", stdout: "  \n\n"},
		{name: "unreachable", status: 255, err: errors.New("ssh: host unreachable"), message: "host unreachable"},
		{name: "authentication", status: 255, err: errors.New("Permission denied (publickey)"), message: "Permission denied"},
		{name: "tmux unavailable", status: 127, stderr: "tmux: command not found\n", message: "tmux: command not found"},
		{name: "session disappeared", status: 1, stderr: "can't find session: work\n", message: "can't find session"},
		{name: "exit without stderr", status: 1, message: "status 1"},
		{name: "output limit", stdout: "PARTIAL_OUTPUT", err: execwrap.ErrOutputLimit, message: "output exceeds limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sh := shell.NewMockShell(t)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
				"-T", "-n", "-o", "BatchMode=yes", "--", "host", remotePreviewCommandFor("work")).
				Return(execwrap.CommandResult{Stdout: []byte(test.stdout), Stderr: []byte(test.stderr), ExitCode: test.status}, test.err).Once()
			p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}},
				shell: sh, tmux: tmux.NewMockTmux(t), previewer: previewer.NewMockPreviewer(t)}
			row := model.SeshSession{Src: "ssh", Name: "ssh/host/work"}
			sessions := model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{"remote": row}}
			m := loadedSourcePreview(t, sessions, p.previewSession)
			if test.message == "" {
				require.NoError(t, m.previewErr)
				require.Contains(t, m.View().Content, "No preview")
			} else {
				require.ErrorContains(t, m.previewErr, test.message)
				require.Empty(t, m.previewContent, "failed capture must not render partial output")
				require.Contains(t, m.View().Content, "Preview unavailable:")
				require.Contains(t, m.View().Content, test.message)
			}
			chosen, quit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.NotNil(t, quit)
			require.Equal(t, row, chosen.(Model).ChosenSession(), "a preview failure cannot block Enter")
		})
	}
}

func TestSSHRemotePreviewTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "already cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
			}
			t.Cleanup(cancel)
			sh := shell.NewMockShell(t)
			started := make(chan context.Context, 1)
			if mode == "already cancelled" {
				cancel()
			} else {
				sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
					"-T", "-n", "-o", "BatchMode=yes", "--", "host", remotePreviewCommandFor("work")).
					RunAndReturn(func(ctx context.Context, _ string, _, _ int, _ ...string) (execwrap.CommandResult, error) {
						started <- ctx
						<-ctx.Done()
						return execwrap.CommandResult{Stdout: []byte("PARTIAL_OUTPUT"), ExitCode: -1}, ctx.Err()
					}).Once()
			}
			p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}}, shell: sh}
			done := make(chan previewLoadedMsg, 1)
			go func() {
				content, err := p.previewSession(ctx, model.SeshSession{Src: "ssh", Name: "ssh/host/work"})
				done <- previewLoadedMsg{content: content, err: err}
			}()
			if mode != "already cancelled" {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("SSH preview did not start")
				}
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case result := <-done:
				want := context.Canceled
				if mode == "timeout" {
					want = context.DeadlineExceeded
				}
				require.ErrorIs(t, result.err, want)
				require.Empty(t, result.content)
			case <-time.After(time.Second):
				t.Fatal("SSH preview did not stop after its context ended")
			}
		})
	}
}

func TestSSHRemotePreviewCacheAndLocalSwitching(t *testing.T) {
	const alpha = "ssh/alpha/work"
	const beta = "ssh/beta/work"
	sessions := model.SeshSessions{OrderedIndex: []string{"alpha", "beta", "local"}, Directory: model.SeshSessionMap{
		"alpha": {Src: "ssh", Name: alpha}, "beta": {Src: "ssh", Name: beta},
		"local": {Src: "tmux", Name: alpha, TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration},
	}}
	sh := shell.NewMockShell(t)
	for _, host := range []string{"alpha", "beta"} {
		sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
			"-T", "-n", "-o", "BatchMode=yes", "--", host, remotePreviewCommandFor("work")).
			Return(execwrap.CommandResult{Stdout: []byte(strings.ToUpper(host) + "_REMOTE")}, nil).Once()
	}
	tm := tmux.NewMockTmux(t)
	tm.EXPECT().ListSessions().Return([]*model.TmuxSession{{ID: "$1", Name: alpha, ServerGeneration: testServerGeneration}}, nil).Times(2)
	tm.EXPECT().CapturePane("$1", testServerGeneration).Return("LOCAL_PANE", nil).Times(2)
	p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"alpha", "beta"}}},
		shell: sh, tmux: tm, previewer: previewer.NewMockPreviewer(t)}
	m := loadedSourcePreview(t, sessions, p.previewSession)
	m.showIcons = true
	require.Contains(t, m.View().Content, "alpha — work")
	require.Contains(t, m.View().Content, "beta — work")
	require.Contains(t, m.previewCache, previewTarget{src: "ssh", name: alpha})
	require.Contains(t, m.View().Content, "ALPHA_REMOTE")
	for _, step := range []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Code: tea.KeyDown}, "BETA_REMOTE"},
		{tea.KeyPressMsg{Code: tea.KeyDown}, "LOCAL_PANE"},
		{tea.KeyPressMsg{Code: tea.KeyUp}, "BETA_REMOTE"},
		{tea.KeyPressMsg{Code: tea.KeyUp}, "ALPHA_REMOTE"},
	} {
		changed, cmd := m.Update(step.key)
		m = loadPreview(t, changed.(Model), cmd)
		require.Contains(t, m.View().Content, step.want)
	}
	m = press(m, ctrlS)
	changed, cmd := m.Update(ctrlS)
	m = loadPreview(t, changed.(Model), cmd)
	require.Equal(t, "tmux", m.sourceFilter)
	require.Contains(t, m.View().Content, "LOCAL_PANE", "a local row with the same Name retains its own identity")
	changed, cmd = m.Update(ctrlS)
	m = loadPreview(t, changed.(Model), cmd)
	require.Equal(t, "", m.sourceFilter)
	require.Contains(t, m.View().Content, "ALPHA_REMOTE")
	sh.AssertNumberOfCalls(t, "CmdCaptureRaw", 2)
	require.Len(t, m.previewCache, 2)
	require.Contains(t, m.previewCache, previewTarget{src: "ssh", name: beta})
}

func TestSSHRemotePreviewEnterStillCreatesAttachBridge(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "preview succeeds", true: "preview fails"}[failure], func(t *testing.T) {
			config := model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}}
			sh := shell.NewMockShell(t)
			var previewErr error
			if failure {
				previewErr = errors.New("SSH preview failed")
			}
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
				"-T", "-n", "-o", "BatchMode=yes", "--", "host", remotePreviewCommandFor("work")).
				Return(execwrap.CommandResult{Stdout: []byte("REMOTE_PANE")}, previewErr).Once()
			tm := tmux.NewMockTmux(t)
			p := &RealPicker{config: config, shell: sh, tmux: tm}
			row := model.SeshSession{Src: "ssh", Name: "ssh/host/work"}
			sessions := model.SeshSessions{OrderedIndex: []string{"remote"}, Directory: model.SeshSessionMap{"remote": row}}
			m := loadedSourcePreview(t, sessions, p.previewSession)
			m.showIcons = true
			require.Contains(t, m.View().Content, "host — work")
			require.Contains(t, iconRow(t, m, "host — work"), "> ⚡ host — work")
			require.Empty(t, tm.Calls, "preview creates no bridge and performs no local tmux operation")
			chosen, quit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.NotNil(t, quit)
			require.Equal(t, row, chosen.(Model).ChosenSession())
			tm.EXPECT().ListSessions().Return(nil, nil).Once()
			tm.EXPECT().NewSessionWithCommand(mock.MatchedBy(func(opts model.TmuxSessionOpts) bool {
				return opts.Command == `exec ssh -tt -- 'host' 'tmux attach-session -t '\''=work'\'''` &&
					len(opts.Environment) == 1 && opts.Environment[0] == model.SSHBridgeIdentityEnv+"="+row.Name
			})).Return("$8 %9", nil).Once()
			tm.EXPECT().SwitchOrAttach("$8", model.ConnectOpts{}).Return("", nil).Once()
			c := connector.NewConnector(config, nil, nil, nil, nil, nil, tm, nil, nil, nil)
			output, err := c.ConnectSession(chosen.(Model).ChosenSession(), model.ConnectOpts{})
			require.NoError(t, err)
			require.Equal(t, "connected to SSH session: "+row.Name, output)
		})
	}
}

func TestSSHRemotePreviewRapidNavigationCancelsAndSerializes(t *testing.T) {
	sessions := model.SeshSessions{OrderedIndex: []string{"a", "b", "local"}, Directory: model.SeshSessionMap{
		"a": {Src: "ssh", Name: "ssh/host/a"}, "b": {Src: "ssh", Name: "ssh/host/b"},
		"local": {Src: "tmux", Name: "local", TmuxSessionID: "$1", TmuxServerGeneration: testServerGeneration},
	}}
	sh := shell.NewMockShell(t)
	started := make(chan context.Context, 1)
	release := make(chan struct{}, 1)
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
		"-T", "-n", "-o", "BatchMode=yes", "--", "host", remotePreviewCommandFor("a")).
		RunAndReturn(func(ctx context.Context, _ string, _, _ int, _ ...string) (execwrap.CommandResult, error) {
			started <- ctx
			<-release
			return execwrap.CommandResult{Stdout: []byte("LATE_REMOTE_OUTPUT")}, nil
		}).Once()
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
		"-T", "-n", "-o", "BatchMode=yes", "--", "host", remotePreviewCommandFor("b")).
		Return(execwrap.CommandResult{Stdout: []byte("CURRENT_REMOTE_OUTPUT")}, nil).Once()
	tm := tmux.NewMockTmux(t)
	tm.EXPECT().ListSessions().Return([]*model.TmuxSession{{ID: "$1", Name: "local", ServerGeneration: testServerGeneration}}, nil).Once()
	tm.EXPECT().CapturePane("$1", testServerGeneration).Return("LOCAL_PANE", nil).Once()
	p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}}, shell: sh, tmux: tm}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		select {
		case release <- struct{}{}:
		default:
		}
	})
	m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
		o.Context, o.Preview, o.PreviewFunc = ctx, true, p.previewSession
	}))
	m.width, m.height = 120, 24
	loaded, cmd := m.Update(sessionsLoadedMsg{sessions: sessions})
	m = loaded.(Model)
	fetch := previewFetch(cmd)
	require.NotNil(t, fetch)
	loaded, run := m.Update(*fetch)
	m = loaded.(Model)
	oldDone := make(chan previewLoadedMsg, 1)
	go func() { oldDone <- run().(previewLoadedMsg) }()
	var oldCtx context.Context
	select {
	case oldCtx = <-started:
	case <-time.After(time.Second):
		t.Fatal("initial SSH capture did not start")
	}

	// Navigation returns while SSH is still held; the next SSH waits for its slot.
	changed, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = changed.(Model)
	require.ErrorIs(t, oldCtx.Err(), context.Canceled)
	fetch = previewFetch(cmd)
	require.NotNil(t, fetch)
	changed, queued := m.Update(*fetch)
	m = changed.(Model)
	queuedDone := make(chan previewLoadedMsg, 1)
	go func() { queuedDone <- queued().(previewLoadedMsg) }()
	require.Len(t, m.previewSSHSlot, 1)
	sh.AssertNumberOfCalls(t, "CmdCaptureRaw", 1)

	changed, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = loadPreview(t, changed.(Model), cmd)
	require.Contains(t, m.View().Content, "LOCAL_PANE", "local previews do not wait for SSH")
	queuedResult := awaitRemotePreview(t, queuedDone)
	require.ErrorIs(t, queuedResult.err, context.Canceled)
	sh.AssertNumberOfCalls(t, "CmdCaptureRaw", 1)

	// Rapid source changes invalidate both pending ticks and running captures.
	changed, obsoleteTick := m.Update(ctrlS)
	m = changed.(Model)
	changed, cmd = m.Update(ctrlS)
	m = changed.(Model)
	require.Nil(t, cmd, "returning to the loaded local identity uses its existing cache")
	fetch = previewFetch(obsoleteTick)
	require.NotNil(t, fetch)
	_, obsolete := m.Update(*fetch)
	require.Nil(t, obsolete)
	require.Contains(t, m.View().Content, "LOCAL_PANE")
	m, _ = typeFilter(m, "ssh/host/b")
	require.Empty(t, m.filtered)
	require.Contains(t, m.View().Content, "No preview")
	changed, cmd = m.Update(ctrlS)
	m = changed.(Model)
	require.Equal(t, "", m.sourceFilter)
	require.Equal(t, "ssh/host/b", m.filterInput.Value())
	require.Contains(t, m.View().Content, "Loading preview...")
	fetch = previewFetch(cmd)
	require.NotNil(t, fetch)
	changed, current := m.Update(*fetch)
	m = changed.(Model)
	currentDone := make(chan previewLoadedMsg, 1)
	go func() { currentDone <- current().(previewLoadedMsg) }()
	sh.AssertNumberOfCalls(t, "CmdCaptureRaw", 1)
	release <- struct{}{}
	newResult := awaitRemotePreview(t, currentDone)
	oldResult := awaitRemotePreview(t, oldDone)
	require.ErrorIs(t, oldResult.err, context.Canceled)
	changed, _ = m.Update(newResult)
	m = changed.(Model)
	for _, late := range []previewLoadedMsg{oldResult, queuedResult} {
		changed, _ = m.Update(late)
		m = changed.(Model)
		require.NoError(t, m.previewErr)
		require.Contains(t, m.View().Content, "CURRENT_REMOTE_OUTPUT")
		require.NotContains(t, m.View().Content, "LATE_REMOTE_OUTPUT")
	}
	require.Len(t, m.previewCache, 1, "cancelled or stale results cannot enter the cache")
	sh.AssertNumberOfCalls(t, "CmdCaptureRaw", 2)
}

func awaitRemotePreview(t *testing.T, done <-chan previewLoadedMsg) previewLoadedMsg {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(time.Second):
		t.Fatal("preview command did not finish")
		return previewLoadedMsg{}
	}
}

func TestSSHRemotePreviewLifecycleCancelsPendingCapture(t *testing.T) {
	for _, mode := range []string{"Enter", "Escape", "Ctrl-c", "toggle off", "query changes", "query selects another SSH", "parent cancellation"} {
		t.Run(mode, func(t *testing.T) {
			row := model.SeshSession{Src: "ssh", Name: "ssh/host/work"}
			sessions := model.SeshSessions{OrderedIndex: []string{"remote", "other"}, Directory: model.SeshSessionMap{
				"remote": row, "other": {Src: "ssh", Name: "ssh/host/deploy"},
			}}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			sh := shell.NewMockShell(t)
			started := make(chan context.Context, 1)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
				"-T", "-n", "-o", "BatchMode=yes", "--", "host", remotePreviewCommandFor("work")).
				RunAndReturn(func(ctx context.Context, _ string, _, _ int, _ ...string) (execwrap.CommandResult, error) {
					started <- ctx
					<-ctx.Done()
					return execwrap.CommandResult{ExitCode: -1}, ctx.Err()
				}).Once()
			p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}}, shell: sh}
			m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
				o.Context, o.Preview, o.PreviewFunc = ctx, true, p.previewSession
			}))
			loaded, cmd := m.Update(sessionsLoadedMsg{sessions: sessions})
			m = loaded.(Model)
			fetch := previewFetch(cmd)
			require.NotNil(t, fetch)
			loaded, run := m.Update(*fetch)
			m = loaded.(Model)
			done := make(chan previewLoadedMsg, 1)
			go func() { done <- run().(previewLoadedMsg) }()
			var runningCtx context.Context
			select {
			case runningCtx = <-started:
			case <-time.After(time.Second):
				t.Fatal("SSH capture did not start")
			}
			switch mode {
			case "Enter":
				chosen, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				require.Equal(t, row, chosen.(Model).ChosenSession())
			case "Escape":
				m = press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
			case "Ctrl-c":
				m = press(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			case "toggle off":
				m = press(m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			case "query changes":
				m, _ = typeFilter(m, "zz-no-match")
			case "query selects another SSH":
				m, _ = typeFilter(m, "deploy")
			case "parent cancellation":
				cancel()
			}
			require.ErrorIs(t, runningCtx.Err(), context.Canceled)
			result := awaitRemotePreview(t, done)
			require.ErrorIs(t, result.err, context.Canceled)
			require.Empty(t, result.content)
			if mode == "query changes" || mode == "query selects another SSH" {
				changed, _ := m.Update(result)
				m = changed.(Model)
				require.NoError(t, m.previewErr, "cancelled query results must not become the new selection's error")
				require.Empty(t, m.previewCache)
			}
		})
	}
}
