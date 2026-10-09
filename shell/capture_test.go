//go:build unix

package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/stretchr/testify/require"
)

func TestShellRawCapture(t *testing.T) {
	sh := NewShell(execwrap.NewExec(), nil)
	for _, test := range []struct {
		name, stdout, stderr             string
		stdoutLimit, stderrLimit, status int
		limitError                       bool
	}{
		{"raw bytes", "\x00before\n\n\t雪\xff", "notice\n\n", 100, 100, 0, false},
		{"empty", "", "", 0, 0, 0, false},
		{"below", "abc", "xy", 4, 3, 0, false},
		{"exact", "abcd", "xyz", 4, 3, 0, false},
		{"stdout overflow", "abcde", "", 4, 3, 0, true},
		{"stderr overflow", "abcd", "xyzw", 4, 3, 0, true},
		{"partial failure", "partial\n", "failed\n", 100, 100, 23, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			// Files allow NUL and every other byte without relying on argv encoding.
			stdout, stderr := filepath.Join(t.TempDir(), "stdout"), filepath.Join(t.TempDir(), "stderr")
			require.NoError(t, os.WriteFile(stdout, []byte(test.stdout), 0600))
			require.NoError(t, os.WriteFile(stderr, []byte(test.stderr), 0600))
			result, err := sh.CmdCaptureRaw(ctx, os.Args[0], test.stdoutLimit, test.stderrLimit,
				"-test.run=^TestShellCaptureHelperProcess$", "shell-capture-helper", stdout, stderr, strconv.Itoa(test.status))
			require.LessOrEqual(t, len(result.Stdout), test.stdoutLimit)
			require.LessOrEqual(t, cap(result.Stdout), test.stdoutLimit)
			require.LessOrEqual(t, len(result.Stderr), test.stderrLimit)
			require.LessOrEqual(t, cap(result.Stderr), test.stderrLimit)
			if test.limitError {
				require.ErrorIs(t, err, execwrap.ErrOutputLimit)
				return
			}
			require.True(t, bytes.Equal([]byte(test.stdout), result.Stdout))
			require.True(t, bytes.Equal([]byte(test.stderr), result.Stderr))
			require.Equal(t, test.status, result.ExitCode)
			if test.status != 0 {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.ErrorContains(t, err, "failed")
			} else {
				require.NoError(t, err)
			}
		})
	}
	result, err := sh.CmdCaptureRaw(t.Context(), filepath.Join(t.TempDir(), "unavailable"), 10, 10)
	require.Error(t, err)
	require.Equal(t, -1, result.ExitCode)
	require.Empty(t, result.Stdout)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = sh.CmdCaptureRaw(ctx, os.Args[0], 10, 10, "-test.run=^$")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, -1, result.ExitCode)
	_, err = sh.CmdCaptureRaw(t.Context(), os.Args[0], -1, 10, "-test.run=^$")
	require.Error(t, err)
}

func TestShellCmdCaptureRetainsExistingNewlineContract(t *testing.T) {
	stdout, stderr := filepath.Join(t.TempDir(), "stdout"), filepath.Join(t.TempDir(), "stderr")
	require.NoError(t, os.WriteFile(stdout, []byte("value\n\n"), 0600))
	require.NoError(t, os.WriteFile(stderr, nil, 0600))
	output, err := NewShell(execwrap.NewExec(), nil).CmdCapture(os.Args[0],
		"-test.run=^TestShellCaptureHelperProcess$", "shell-capture-helper", stdout, stderr, "0")
	require.NoError(t, err)
	require.Equal(t, "value\n", output)
}

func TestShellCaptureHelperProcess(t *testing.T) {
	i := slices.Index(os.Args, "shell-capture-helper")
	if i < 0 {
		return
	}
	for j, stream := range []*os.File{os.Stdout, os.Stderr} {
		file, err := os.Open(os.Args[i+1+j])
		if err != nil {
			os.Exit(98)
		}
		_, err = io.Copy(stream, file)
		_ = file.Close()
		if err != nil && !errors.Is(err, os.ErrClosed) {
			os.Exit(98)
		}
	}
	status, _ := strconv.Atoi(os.Args[i+3])
	os.Exit(status)
}
