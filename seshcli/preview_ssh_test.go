package seshcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/previewer"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func runSSHPreviewCommand(t *testing.T, config string, input string, sh *shell.MockShell, ctx context.Context, flags ...string) (string, error) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "sesh.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0600))

	base := NewBaseDeps()
	base.Shell = sh
	root := &cobra.Command{Use: "sesh", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().String("config", configPath, "")
	root.AddCommand(NewPreviewCommand(base))
	root.SetArgs(append(append([]string{"preview"}, flags...), input))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	err := root.ExecuteContext(ctx)
	return output.String(), err
}

func TestCLISSHPreviewSuccessAndIconPrefixedInput(t *testing.T) {
	for _, input := range []string{
		"ssh/myinventa/myinventa",
		" ssh/myinventa/deploy",
		"\033[34m\033[39m ssh/myinventa/deploy",
	} {
		t.Run(input, func(t *testing.T) {
			sh := shell.NewMockShell(t)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", previewer.SSHPreviewBytes, previewer.SSHPreviewStderr,
				"-T", "-n", "-o", "BatchMode=yes", "--", "myinventa", mock.Anything).
				Return(execwrap.CommandResult{Stdout: []byte("REMOTE_PANE\n")}, nil).Once()

			output, err := runSSHPreviewCommand(t, "cache = false\n[ssh]\nhosts = ['myinventa']\n", input, sh, context.Background())
			require.NoError(t, err)
			require.Equal(t, "REMOTE_PANE\n", output)
		})
	}
}

func TestCLISSHPreviewRequiresConfiguredHost(t *testing.T) {
	sh := shell.NewMockShell(t)
	output, err := runSSHPreviewCommand(t, "cache = false\n[ssh]\nhosts = ['other']\n", "ssh/myinventa/deploy", sh, context.Background())

	require.ErrorContains(t, err, "not configured")
	require.Empty(t, output)
	require.Empty(t, sh.Calls)
}

func TestCLISSHPreviewEmptyCaptureDoesNotFallThrough(t *testing.T) {
	sh := shell.NewMockShell(t)
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", previewer.SSHPreviewBytes, previewer.SSHPreviewStderr,
		"-T", "-n", "-o", "BatchMode=yes", "--", "myinventa", mock.Anything).
		Return(execwrap.CommandResult{}, nil).Once()
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", mock.Anything, mock.Anything,
		"-u", "list-sessions", "-F", mock.Anything).
		Return(execwrap.CommandResult{}, errors.New("local tmux must not be queried")).Maybe()
	sh.EXPECT().ShellCmd("printf COLLIDING", map[string]string{"{}": "/colliding"}).
		Return("COLLIDING", nil).Maybe()

	output, err := runSSHPreviewCommand(t, `cache = false
[ssh]
hosts = ['myinventa']
[[session]]
name = 'ssh/myinventa/deploy'
path = '/colliding'
preview_command = 'printf COLLIDING'
`, "ssh/myinventa/deploy", sh, context.Background())

	require.NoError(t, err)
	require.Empty(t, output)
	sh.AssertNotCalled(t, "ShellCmd", "printf COLLIDING", map[string]string{"{}": "/colliding"})
}

func TestCLISSHPreviewCancellationStopsCapture(t *testing.T) {
	sh := shell.NewMockShell(t)
	started := make(chan struct{})
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", previewer.SSHPreviewBytes, previewer.SSHPreviewStderr,
		"-T", "-n", "-o", "BatchMode=yes", "--", "myinventa", mock.Anything).
		RunAndReturn(func(ctx context.Context, _ string, _, _ int, _ ...string) (execwrap.CommandResult, error) {
			close(started)
			<-ctx.Done()
			return execwrap.CommandResult{ExitCode: -1}, ctx.Err()
		}).Once()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runSSHPreviewCommand(t, "cache = false\n[ssh]\nhosts = ['myinventa']\n", "ssh/myinventa/deploy", sh, ctx)
		done <- err
	}()
	<-started
	cancel()

	select {
	case err := <-done:
		require.True(t, errors.Is(err, context.Canceled), "CLI preview error: %v", err)
	case <-time.After(time.Second):
		t.Fatal("CLI preview did not stop after cancellation")
	}
}

func TestCLISSHPreviewWatchUsesSharedCapture(t *testing.T) {
	sh := shell.NewMockShell(t)
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", previewer.SSHPreviewBytes, previewer.SSHPreviewStderr,
		"-T", "-n", "-o", "BatchMode=yes", "--", "myinventa", mock.Anything).
		Return(execwrap.CommandResult{Stdout: []byte("REMOTE_PANE\n")}, nil).Once()

	output, err := runSSHPreviewCommand(t, "cache = false\n[ssh]\nhosts = ['myinventa']\n", "ssh/myinventa/deploy", sh, t.Context(), "--watch")

	require.NoError(t, err)
	require.Equal(t, "REMOTE_PANE\n", output)
	require.Len(t, sh.Calls, 1, "watch must not query local tmux or poll SSH")
}

func TestCLIPreviewWatchConfiguredAndLightningDirectory(t *testing.T) {
	parent := t.TempDir()
	const directoryName = "⚡ directory"
	directory := filepath.Join(parent, directoryName)
	require.NoError(t, os.Mkdir(directory, 0700))
	t.Chdir(parent)
	for _, test := range []struct {
		name, config, input, command, path, output string
	}{
		{name: "configured", config: "cache = false\n[[session]]\nname = 'configured'\npath = '/configured'\npreview_command = 'printf CONFIGURED'\n",
			input: "configured", command: "printf CONFIGURED", path: "/configured", output: "CONFIGURED"},
		{name: "lightning directory", config: "cache = false\n", input: directoryName, command: "ls {}", path: directory, output: "DIRECTORY"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sh := shell.NewMockShell(t)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", mock.Anything, mock.Anything,
				"-u", "list-sessions", "-F", mock.Anything).
				Return(execwrap.CommandResult{}, nil)
			sh.EXPECT().ShellCmd(test.command, map[string]string{"{}": test.path}).Return(test.output, nil).Once()

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			output, err := runSSHPreviewCommand(t, test.config, test.input, sh, ctx, "--watch")

			require.NoError(t, err)
			require.Equal(t, test.output, output)
			require.NoError(t, ctx.Err(), "non-tmux watch must remain one-shot")
		})
	}
}

func TestCLIPreviewWatchRejectsNonPositiveIntervals(t *testing.T) {
	for _, input := range []string{"local", "ssh/dev/work"} {
		for _, interval := range []string{"0s", "-1ms"} {
			t.Run(input+"/"+interval, func(t *testing.T) {
				sh := shell.NewMockShell(t)
				output, err := runSSHPreviewCommand(t, "cache = false\n[ssh]\nhosts = ['dev']\n", input, sh, t.Context(), "--watch", "--interval", interval)

				require.ErrorContains(t, err, "preview interval must be greater than zero")
				require.Empty(t, output)
				require.Empty(t, sh.Calls)
			})
		}
	}
}
