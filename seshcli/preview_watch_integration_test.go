//go:build unix

package seshcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/icon"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/previewer"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Only SSH transport is replaced; local tmux calls reach the existing private fixture.
type watchSSHTransport struct {
	shell.Shell
	capture      func(context.Context) (execwrap.CommandResult, error)
	sshCalls     int
	localLookups int
}

func (s *watchSSHTransport) CmdCaptureRaw(ctx context.Context, name string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
	if name == "ssh" {
		s.sshCalls++
		return s.capture(ctx)
	}
	s.localLookups++
	return s.Shell.CmdCaptureRaw(ctx, name, stdoutLimit, stderrLimit, args...)
}

type watchPreviewWriter struct {
	bytes.Buffer
	afterWrite func()
}

func (w *watchPreviewWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if w.afterWrite != nil {
		w.afterWrite()
	}
	return n, err
}

func nativeWatchPreviewCommand(t *testing.T, s *existingPrivateTmux, sh shell.Shell, input, host string) *cobra.Command {
	t.Helper()
	base := NewBaseDeps()
	base.Shell = sh
	root := s.commandRoot(NewPreviewCommand(base))
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "sesh.toml"), []byte("cache = false\n[ssh]\nhosts = ['"+host+"']\n"), 0600))
	root.SetArgs([]string{"preview", "--watch", "--interval", "1ms", input})
	return root
}

func TestCLISSHWatchIdentityCollisionNative(t *testing.T) {
	s := newExistingPrivateTmux(t)
	s.newSession("$2", "ssh/dev/work", "original", "COLLIDING_LOCAL")
	before := s.state("$2")
	sh := &watchSSHTransport{Shell: s, capture: func(context.Context) (execwrap.CommandResult, error) {
		return execwrap.CommandResult{Stdout: []byte("REMOTE_PANE\n")}, nil
	}}
	root := nativeWatchPreviewCommand(t, s, sh, "ssh/dev/work", "dev")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output := &watchPreviewWriter{afterWrite: cancel}
	root.SetOut(output)

	require.NoError(t, root.ExecuteContext(ctx))
	require.Equal(t, "REMOTE_PANE\n", output.String())
	require.NotContains(t, output.String(), "COLLIDING_LOCAL")
	require.Equal(t, 1, sh.sshCalls, "SSH watch must capture only once")
	require.Zero(t, sh.localLookups, "SSH watch must not look up a local session")
	require.Equal(t, before, s.state("$2"))
}

func TestCLISSHWatchResultsStayExclusiveNative(t *testing.T) {
	const identity = "ssh/dev/work"
	ic := icon.NewIcon(model.Config{})
	row := model.SeshSession{Src: "tmux", Name: identity}
	captureError := errors.New("SSH capture failed")
	for _, test := range []struct {
		name, identity, input, host string
		output                      string
		captureError                error
		wantError                   bool
		wantSSHCalls                int
	}{
		{name: "nonempty", identity: identity, input: identity, host: "dev", output: "REMOTE_PANE\n", wantSSHCalls: 1},
		{name: "empty", identity: identity, input: identity, host: "dev", wantSSHCalls: 1},
		{name: "SSH error", identity: identity, input: identity, host: "dev", captureError: captureError, wantError: true, wantSSHCalls: 1},
		{name: "unconfigured host", identity: identity, input: identity, host: "other", wantError: true},
		{name: "missing component", identity: "ssh/dev", input: "ssh/dev", host: "dev", wantError: true},
		{name: "malformed escape", identity: "ssh/dev/%GG", input: "ssh/dev/%GG", host: "dev", wantError: true},
		{name: "noncanonical escape", identity: "ssh/dev/work%2fchild", input: "ssh/dev/work%2fchild", host: "dev", wantError: true},
		{name: "plain source icon", identity: identity, input: ic.AddIconNoColor(row), host: "dev", output: "REMOTE_PANE\n", wantSSHCalls: 1},
		{name: "ANSI source icon", identity: identity, input: ic.AddIcon(row), host: "dev", output: "REMOTE_PANE\n", wantSSHCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newExistingPrivateTmux(t)
			s.newSession("$2", test.identity, "original", "COLLIDING_LOCAL")
			before := s.state("$2")
			sh := &watchSSHTransport{Shell: s, capture: func(context.Context) (execwrap.CommandResult, error) {
				return execwrap.CommandResult{Stdout: []byte(test.output)}, test.captureError
			}}
			root := nativeWatchPreviewCommand(t, s, sh, test.input, test.host)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var output, stderr bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&stderr)

			err := root.ExecuteContext(ctx)

			if test.wantError {
				require.Error(t, err)
				require.Empty(t, output.String())
				if test.captureError != nil {
					require.ErrorIs(t, err, test.captureError)
				}
				if test.host == "other" {
					require.ErrorContains(t, err, "not configured")
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, test.output, output.String())
			}
			require.NoError(t, ctx.Err(), "SSH watch must return without waiting for cancellation")
			require.NotContains(t, output.String(), "COLLIDING_LOCAL")
			require.Empty(t, stderr.String())
			require.Equal(t, test.wantSSHCalls, sh.sshCalls)
			require.Zero(t, sh.localLookups)
			require.Equal(t, before, s.state("$2"))
		})
	}
}

func TestCLILocalWatchRefreshAndLightningNameNative(t *testing.T) {
	for _, name := range []string{"local", "⚡ foo"} {
		t.Run(name, func(t *testing.T) {
			s := newExistingPrivateTmux(t)
			s.newSession("$2", name, "original", "LOCAL_FIRST")
			sh := &watchSSHTransport{Shell: s, capture: func(context.Context) (execwrap.CommandResult, error) {
				t.Fatal("local watch must not invoke SSH")
				return execwrap.CommandResult{}, nil
			}}
			root := nativeWatchPreviewCommand(t, s, sh, name, "dev")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			s.afterCapture = func(frame int) {
				if frame == 1 {
					s.must("respawn-pane", "-k", "-t", "$2:", "--", "/bin/sh", "-c", "printf 'LOCAL_NEXT\\n'; exec sleep 120")
					s.ready("$2", "LOCAL_NEXT")
				} else {
					cancel()
				}
			}
			var output bytes.Buffer
			root.SetOut(&output)

			require.NoError(t, root.ExecuteContext(ctx))
			require.Contains(t, output.String(), "LOCAL_FIRST")
			require.Contains(t, output.String(), previewClearScreen+"LOCAL_NEXT")
			require.GreaterOrEqual(t, s.captures, 2)
			require.Zero(t, sh.sshCalls)
			require.Equal(t, name, s.must("display-message", "-p", "-t", "$2:", "#{session_name}"))
		})
	}
}

func TestCLISSHWatchCancellationReapsCaptureNative(t *testing.T) {
	s := newExistingPrivateTmux(t)
	s.newSession("$2", "ssh/dev/work", "original", "COLLIDING_LOCAL")
	before := s.state("$2")
	pidFile := filepath.Join(s.dir, "ssh-pid")
	sh := &watchSSHTransport{Shell: s, capture: func(ctx context.Context) (execwrap.CommandResult, error) {
		return shell.NewShell(execwrap.NewExec(), nil).CmdCaptureRaw(ctx, "/bin/sh", previewer.SSHPreviewBytes, previewer.SSHPreviewStderr,
			"-c", "printf '%s' \"$$\" > "+shell.Quote(pidFile)+"; exec sleep 120")
	}}
	root := nativeWatchPreviewCommand(t, s, sh, "ssh/dev/work", "dev")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	var output bytes.Buffer
	root.SetOut(&output)
	done := make(chan error, 1)
	finished := false
	t.Cleanup(func() {
		cancel()
		if !finished {
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Error("SSH preview did not finish during bounded cleanup")
			}
		}
	})
	go func() { done <- root.ExecuteContext(ctx) }()
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 0
	}, 2*time.Second, 10*time.Millisecond)
	group, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	require.Equal(t, pid, group, "capture must use its own process group")
	cancel()
	select {
	case err := <-done:
		finished = true
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("SSH preview did not stop after cancellation")
	}
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "capture must be terminated and reaped before the CLI returns")
	require.Empty(t, output.String())
	require.Equal(t, 1, sh.sshCalls)
	require.Zero(t, sh.localLookups)
	require.Equal(t, before, s.state("$2"))
}
