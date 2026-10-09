//go:build unix

package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The stub changes only emitted bytes/status. RealShell runs the configured
// executable, captures raw bounded streams, and the production inventory parses.
func inventoryCaptureExecutable(t *testing.T, output, diagnostic string, status int) string {
	t.Helper()
	dir := t.TempDir()
	stdout, stderr, binary := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr"), filepath.Join(dir, "configured-tmux")
	require.NoError(t, os.WriteFile(stdout, []byte(output), 0600))
	require.NoError(t, os.WriteFile(stderr, []byte(diagnostic), 0600))
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = show-environment ]; then printf '%s\\n' '-SESH_SSH_IDENTITY'; exit 0; fi\n" +
		"[ \"$1\" = -u ] || exit 97\n" +
		"case \"$2\" in list-sessions|list-panes) ;; *) exit 97 ;; esac\n" +
		"/bin/cat " + shell.Quote(stdout) + "\n/bin/cat " + shell.Quote(stderr) + " >&2\nexit " + strconv.Itoa(status) + "\n"
	require.NoError(t, os.WriteFile(binary, []byte(script), 0700))
	return binary
}

func TestInventoryCaptureProductionBoundary(t *testing.T) {
	for _, kind := range []string{"sessions", "panes"} {
		t.Run(kind, func(t *testing.T) {
			fields, row, display := sessionInventoryFields(), sessionInventoryRow, 17
			if kind == "panes" {
				fields, row, display = paneInventoryFields(), paneInventoryRow, 1
			}
			collect := func(t *testing.T, binary string) ([]string, error) {
				tm := NewTmux(nil, shell.NewShell(execwrap.NewExec(), nil), binary)
				if kind == "sessions" {
					sessions, err := tm.ListSessions()
					if err != nil {
						require.Nil(t, sessions)
						return nil, err
					}
					names := make([]string, len(sessions))
					for i, session := range sessions {
						require.Equal(t, "$1", session.ID)
						names[i] = session.Name
					}
					return names, nil
				}
				panes, err := tm.ListTmuxPanes()
				if err != nil {
					require.Nil(t, panes)
					return nil, err
				}
				names := make([]string, len(panes))
				for i, pane := range panes {
					require.Equal(t, fields[7], pane.SessionID)
					require.Equal(t, fields[8], pane.WindowID)
					require.Equal(t, fields[6], pane.PaneID)
					names[i] = pane.WindowName
				}
				return names, nil
			}
			valid := row(fields)
			for _, test := range []struct {
				name, output, stderr string
				status               int
				accepted             bool
			}{
				{"normal terminator", valid + "\n", "notice\n", 0, true},
				{"empty success", "", "", 0, true},
				{"extra blank record", valid + "\n\n", "", 0, false},
				{"blank middle record", valid + "\n\n" + valid + "\n", "", 0, false},
				{"unexpected trailing data", valid + "\nextra", "", 0, false},
				{"incomplete later record", valid + "\nP1::", "", 0, false},
				{"missing server", "", "no server running on /tmp/private42\n", 1, true},
				{"missing socket", "", "error connecting to /tmp/private42 (No such file or directory)\n", 1, true},
				{"no sessions", "", "no sessions\n", 1, true},
				{"unknown empty failure", "", "", 1, false},
				{"permission failure", "", "error connecting to /tmp/private42 (Permission denied)\n", 1, false},
				{"refused connection", "", "error connecting to /tmp/private42 (Connection refused)\n", 1, false},
				{"unexpected status", "", "no server running on /tmp/private42\n", 23, false},
				{"additional diagnostic", "", "no server running on /tmp/private42\nother error\n", 1, false},
				{"partial failure", valid + "\n", "failed\n", 23, false},
				{"partial absence failure", valid + "\n", "no server running on /tmp/private42\n", 1, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					names, err := collect(t, inventoryCaptureExecutable(t, test.output, test.stderr, test.status))
					if !test.accepted {
						require.Error(t, err)
						require.Nil(t, names)
						return
					}
					require.NoError(t, err)
					if test.output == "" {
						require.Empty(t, names)
					} else {
						require.Equal(t, []string{fields[display]}, names)
					}
				})
			}
			t.Run("embedded newline", func(t *testing.T) {
				values := append([]string(nil), fields...)
				values[display] = "before\n\n::\t雪 %0 $0 @0\nafter"
				names, err := collect(t, inventoryCaptureExecutable(t, row(values)+"\n"+valid+"\n", "", 0))
				require.NoError(t, err)
				require.ElementsMatch(t, []string{values[display], fields[display]}, names)
			})
			t.Run("unavailable executable", func(t *testing.T) {
				names, err := collect(t, filepath.Join(t.TempDir(), "missing"))
				require.Error(t, err)
				require.Nil(t, names)
			})
			for _, extra := range []int{-1, 0, 1} {
				t.Run("wire limit "+strconv.Itoa(extra), func(t *testing.T) {
					values := append([]string(nil), fields...)
					values[display] = ""
					values[display] = strings.Repeat("x", maxInventoryBytes-len(row(values)+"\n")+extra)
					output := row(values) + "\n"
					require.Equal(t, maxInventoryBytes+extra, len(output))
					names, err := collect(t, inventoryCaptureExecutable(t, output, "", 0))
					if extra > 0 {
						require.ErrorIs(t, err, execwrap.ErrOutputLimit)
						require.Nil(t, names)
					} else {
						require.NoError(t, err)
						require.Equal(t, []string{values[display]}, names)
					}
				})
			}
			t.Run("encoded escaping exceeds limit", func(t *testing.T) {
				values := append([]string(nil), fields...)
				values[display] = strings.Repeat(" ", maxInventoryBytes/2)
				names, err := collect(t, inventoryCaptureExecutable(t, row(values)+"\n", "", 0))
				require.ErrorIs(t, err, execwrap.ErrOutputLimit)
				require.Nil(t, names)
			})
			t.Run("stderr limit", func(t *testing.T) {
				names, err := collect(t, inventoryCaptureExecutable(t, valid+"\n", strings.Repeat("x", maxInventoryStderr+1), 0))
				require.ErrorIs(t, err, execwrap.ErrOutputLimit)
				require.Nil(t, names)
			})
		})
	}
}

func TestInventoryCaptureRejectsFailedAndUnverifiedResultsBeforeOwnership(t *testing.T) {
	for _, kind := range []string{"sessions", "panes"} {
		for _, failure := range []string{"exit", "limit", "cancel", "status only"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				s := shell.NewMockShell(t)
				output, args := sessionInventoryRow(sessionInventoryFields()), []any{"-u", "list-sessions", "-F", listsessionsformat()}
				if kind == "panes" {
					output, args = paneInventoryRow(paneInventoryFields()), []any{"-u", "list-panes", "-s", "-F", listpanesformat()}
				}
				result, err := rawInventoryResult(output+"\n"), errors.New("failed command")
				if failure == "limit" {
					err = execwrap.ErrOutputLimit
				}
				if failure == "cancel" {
					err = context.Canceled
				}
				if failure == "status only" {
					result.ExitCode = 23
					err = nil
				}
				s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, args...).Return(result, err).Once()
				tm := NewTmux(nil, s, "custom-tmux")
				if kind == "sessions" {
					records, err := tm.ListSessions()
					require.Error(t, err)
					require.Nil(t, records)
				} else {
					records, err := tm.ListTmuxPanes()
					require.Error(t, err)
					require.Nil(t, records)
				}
			})
		}
	}
}

func TestPaneInventoryEmptyTargetRequiresEmptySessionProbe(t *testing.T) {
	for _, probe := range []string{"", sessionInventoryRow(sessionInventoryFields()) + "\n", "\n", "malformed"} {
		t.Run(strconv.Itoa(len(probe)), func(t *testing.T) {
			s := shell.NewMockShell(t)
			err := environmentExitError(t)
			s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-panes", "-s", "-F", listpanesformat()).Return(execwrap.CommandResult{Stderr: []byte("no current target\n"), ExitCode: 1}, err).Once()
			s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-sessions", "-F", listsessionsformat()).Return(rawInventoryResult(probe), nil).Once()
			panes, gotErr := NewTmux(nil, s, "custom-tmux").ListTmuxPanes()
			if probe == "" {
				require.NoError(t, gotErr)
				require.Empty(t, panes)
			} else {
				require.ErrorIs(t, gotErr, err)
				require.Nil(t, panes)
			}
		})
	}
}

func TestPaneInventoryEmptyTargetPreservesSessionProbeFailures(t *testing.T) {
	exitErr := environmentExitError(t)
	executionErr := errors.New("session probe execution failed")
	for _, message := range []string{"no current target", "no current session"} {
		for _, test := range []struct {
			name, output string
			cause        error
		}{
			{"canceled", "", context.Canceled},
			{"deadline", "", context.DeadlineExceeded},
			{"output limit", "", execwrap.ErrOutputLimit},
			{"execution", "", executionErr},
			{"partial stdout", sessionInventoryRow(sessionInventoryFields()) + "\n", executionErr},
		} {
			t.Run(message+"/"+test.name, func(t *testing.T) {
				s := shell.NewMockShell(t)
				paneErr := fmt.Errorf("custom-tmux: %w: %s", exitErr, message)
				probeErr := fmt.Errorf("custom-tmux: %w", test.cause)
				s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-panes", "-s", "-F", listpanesformat()).Return(execwrap.CommandResult{Stderr: []byte(message + "\n"), ExitCode: 1}, paneErr).Once()
				s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-sessions", "-F", listsessionsformat()).Return(rawInventoryResult(test.output), probeErr).Once()

				panes, err := NewTmux(nil, s, "custom-tmux").ListTmuxPanes()

				require.Nil(t, panes)
				require.ErrorIs(t, err, test.cause)
				require.ErrorIs(t, err, probeErr)
				require.ErrorIs(t, err, paneErr)
				require.ErrorContains(t, err, "session inventory probe failed")
				require.ErrorContains(t, err, message)
				s.AssertNumberOfCalls(t, "CmdCaptureRaw", 2)
			})
		}
	}
}

func TestPaneInventoryUnrelatedFailureDoesNotProbeSessions(t *testing.T) {
	s := shell.NewMockShell(t)
	paneErr := fmt.Errorf("custom-tmux: %w: can't find session: missing", environmentExitError(t))
	s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-panes", "-s", "-F", listpanesformat()).Return(execwrap.CommandResult{Stderr: []byte("can't find session: missing\n"), ExitCode: 1}, paneErr).Once()

	panes, err := NewTmux(nil, s, "custom-tmux").ListTmuxPanes()

	require.Nil(t, panes)
	require.Same(t, paneErr, err)
	s.AssertNumberOfCalls(t, "CmdCaptureRaw", 1)
}

func TestInventoryCaptureAbsenceCannotSuppressCancellation(t *testing.T) {
	exitErr := environmentExitError(t)
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, execwrap.ErrOutputLimit} {
		for _, message := range []string{"no server running on /tmp/private42\n", "no sessions\n", "no current target\n"} {
			for _, kind := range []string{"sessions", "panes"} {
				t.Run(kind+"/"+cause.Error()+"/"+message, func(t *testing.T) {
					s := shell.NewMockShell(t)
					args := []any{"-u", "list-sessions", "-F", listsessionsformat()}
					if kind == "panes" {
						args = []any{"-u", "list-panes", "-s", "-F", listpanesformat()}
					}
					s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, args...).Return(execwrap.CommandResult{Stderr: []byte(message), ExitCode: 1}, errors.Join(exitErr, cause)).Once()
					tm := NewTmux(nil, s, "custom-tmux")
					if kind == "sessions" {
						records, err := tm.ListSessions()
						require.ErrorIs(t, err, cause)
						require.Nil(t, records)
					} else {
						records, err := tm.ListTmuxPanes()
						require.ErrorIs(t, err, cause)
						require.Nil(t, records)
					}
				})
			}
		}
	}
}
