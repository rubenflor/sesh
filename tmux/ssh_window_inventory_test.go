package tmux

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Fixture geometry is literal, independent of the production schema/format.
func sshWindowTestRow(fields []string) string {
	fields = append([]string(nil), fields...)
	for _, i := range []int{2, 6, 7, 8} {
		fields[i] = quoteInventoryField(fields[i])
	}
	return "RW1::" + strings.Join(fields, "::") + "\n"
}

func sshWindowTestRows() [][]string {
	return [][]string{
		{"B", "$7", "work", "", "", "", "", "", "101:1700000000:/tmp/remote", "1"},
		{"W", "$7", "work", "@9", "0", "1", "docker", "/srv/project", "101:1700000000:/tmp/remote", "1"},
		{"E", "$7", "work", "", "", "", "", "", "101:1700000000:/tmp/remote", "1"},
	}
}

func sshWindowTestOutput(rows [][]string) string {
	var output strings.Builder
	for _, row := range rows {
		output.WriteString(sshWindowTestRow(row))
	}
	return output.String()
}

func sshWindowTestShell(t *testing.T, result execwrap.CommandResult, err error) *shell.MockShell {
	t.Helper()
	sh := shell.NewMockShell(t)
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", 1<<20, 4<<10,
		"-T", "-n", "-o", "BatchMode=yes", "--", "dev", mock.Anything).
		Return(result, err).Once()
	return sh
}

func TestSSHWindowInventoryLiteralFields(t *testing.T) {
	for _, session := range []struct{ identity, stored string }{
		{"ssh/dev/work", "work"},
		{"ssh/dev/%240", "$0"},
		{"ssh/dev/name%3Awith%3Acolons", "name:with:colons"},
		{"ssh/dev/a%2Fb%25%20caf%C3%A9%E9%9B%AA", "a/b% café雪"},
		{"ssh/dev/a%0Ab%5Cn", `a\nb\\n`},
	} {
		t.Run(session.identity, func(t *testing.T) {
			rows := sshWindowTestRows()
			for _, row := range rows {
				row[2], row[9] = session.stored, "2"
			}
			const name = "dup ' \" /% $HOME \\n café雪\nRW1::W::fake\t\r"
			const path = "/srv/a b/$LITERAL/'\"%\\n雪\nnext"
			rows[1][6], rows[1][7] = name, path
			second := append([]string(nil), rows[1]...)
			second[3], second[4], second[5] = "@10", "3", "0"
			rows = append(rows[:2], second, rows[2])
			sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(sshWindowTestOutput(rows)), Stderr: []byte("nonfatal warning\n")}, nil)

			windows, err := ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, session.identity)

			require.NoError(t, err)
			require.Equal(t, []model.SSHWindow{
				{Name: name, Path: path, Index: 0, Active: true, Src: "ssh", Session: session.identity, SessionID: "$7", ID: "@9", ServerFingerprint: "101:1700000000:/tmp/remote"},
				{Name: name, Path: path, Index: 3, Active: false, Src: "ssh", Session: session.identity, SessionID: "$7", ID: "@10", ServerFingerprint: "101:1700000000:/tmp/remote"},
			}, windows)
			require.Len(t, sh.Calls, 1)
		})
	}
}

func TestSSHWindowInventoryRejectsIdentityBeforeExecution(t *testing.T) {
	for _, identity := range []string{
		"local", "ssh/dev", "ssh//work", "ssh/dev/", "ssh/dev/a/b", "ssh/dev/%zz",
		"ssh/dev/a%2fb", "ssh/dev/%77ork", "ssh/dev/%FF", "ssh/dev/%00", "ssh/other/work", "ssh/de%00v/work",
	} {
		t.Run(identity, func(t *testing.T) {
			sh := shell.NewMockShell(t)
			windows, err := ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev", "de\x00v"}}}, sh, identity)
			require.Error(t, err)
			require.Nil(t, windows)
			require.Empty(t, sh.Calls)
		})
	}
}

func TestSSHWindowInventoryEmpty(t *testing.T) {
	rows := sshWindowTestRows()
	rows[0][9], rows[2][9] = "0", "0"
	sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(sshWindowTestOutput([][]string{rows[0], rows[2]}))}, nil)
	windows, err := ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, "ssh/dev/work")
	require.NoError(t, err)
	require.Equal(t, []model.SSHWindow{}, windows, "empty must encode as [], not null")
}

func TestSSHWindowInventoryRepeatedWindowLinks(t *testing.T) {
	rows := sshWindowTestRows()
	for _, row := range rows {
		row[9] = "2"
	}
	second := append([]string(nil), rows[1]...)
	second[4], second[5] = "3", "0"
	rows = append(rows[:2], second, rows[2])
	sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(sshWindowTestOutput(rows))}, nil)

	windows, err := ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, "ssh/dev/work")

	require.NoError(t, err, "tmux permits multiple links to one native window at different indices")
	require.Len(t, windows, 2)
	require.Equal(t, windows[0].ID, windows[1].ID)
	require.Equal(t, 0, windows[0].Index)
	require.Equal(t, 3, windows[1].Index)
	require.True(t, windows[0].Active)
	require.False(t, windows[1].Active)
}

func TestSSHWindowInventoryRejectsUnverifiedResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([][]string) string
	}{
		{"empty stdout", func([][]string) string { return "" }},
		{"missing begin", func(r [][]string) string { return sshWindowTestOutput(r[1:]) }},
		{"missing end", func(r [][]string) string { return sshWindowTestOutput(r[:2]) }},
		{"missing newline", func(r [][]string) string { return strings.TrimSuffix(sshWindowTestOutput(r), "\n") }},
		{"unterminated quote", func(r [][]string) string { return strings.Replace(sshWindowTestOutput(r), `"docker"`, `"docker`, 1) }},
		{"bad escape", func(r [][]string) string { return strings.Replace(sshWindowTestOutput(r), `"docker"`, `"\z"`, 1) }},
		{"NUL", func(r [][]string) string { r[1][6] = "a\x00b"; return sshWindowTestOutput(r) }},
		{"invalid UTF-8", func(r [][]string) string { r[1][6] = "\xff"; return sshWindowTestOutput(r) }},
		{"banner", func(r [][]string) string { return "unrequested output\n" + sshWindowTestOutput(r) }},
		{"extra end", func(r [][]string) string { return sshWindowTestOutput(r) + sshWindowTestRow(r[2]) }},
		{"foreign parent", func(r [][]string) string { r[1][1] = "$8"; return sshWindowTestOutput(r) }},
		{"foreign session name", func(r [][]string) string { r[1][2] = "other"; return sshWindowTestOutput(r) }},
		{"wrong requested name", func(r [][]string) string {
			for _, row := range r {
				row[2] = "other"
			}
			return sshWindowTestOutput(r)
		}},
		{"restarted during listing", func(r [][]string) string { r[2][8] = "102:1700000001:/tmp/remote"; return sshWindowTestOutput(r) }},
		{"mixed lifetime", func(r [][]string) string { r[1][8] = "102:1700000001:/tmp/remote"; return sshWindowTestOutput(r) }},
		{"metadata payload", func(r [][]string) string { r[0][3], r[2][3] = "@9", "@9"; return sshWindowTestOutput(r) }},
		{"invalid session ID", func(r [][]string) string {
			for _, row := range r {
				row[1] = "$07"
			}
			return sshWindowTestOutput(r)
		}},
		{"invalid fingerprint", func(r [][]string) string {
			for _, row := range r {
				row[8] = "101:0:/tmp/remote"
			}
			return sshWindowTestOutput(r)
		}},
		{"invalid window ID", func(r [][]string) string { r[1][3] = "@09"; return sshWindowTestOutput(r) }},
		{"negative index", func(r [][]string) string { r[1][4] = "-1"; return sshWindowTestOutput(r) }},
		{"noncanonical index", func(r [][]string) string { r[1][4] = "00"; return sshWindowTestOutput(r) }},
		{"invalid active", func(r [][]string) string { r[1][5] = "2"; return sshWindowTestOutput(r) }},
		{"no active window", func(r [][]string) string { r[1][5] = "0"; return sshWindowTestOutput(r) }},
		{"unexpected record kind", func(r [][]string) string { r[1][0] = "B"; return sshWindowTestOutput(r) }},
		{"missing window", func(r [][]string) string { return sshWindowTestOutput([][]string{r[0], r[2]}) }},
		{"noncanonical count", func(r [][]string) string {
			for _, row := range r {
				row[9] = "01"
			}
			return sshWindowTestOutput(r)
		}},
		{"changed topology", func(r [][]string) string { r[2][9] = "2"; return sshWindowTestOutput(r) }},
		{"duplicate index", func(r [][]string) string {
			for _, row := range r {
				row[9] = "2"
			}
			second := append([]string(nil), r[1]...)
			second[3], second[5] = "@10", "0"
			return sshWindowTestOutput(append(r[:2], second, r[2]))
		}},
		{"two active windows", func(r [][]string) string {
			for _, row := range r {
				row[9] = "2"
			}
			second := append([]string(nil), r[1]...)
			second[3], second[4] = "@10", "1"
			return sshWindowTestOutput(append(r[:2], second, r[2]))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(test.edit(sshWindowTestRows()))}, nil)
			windows, err := ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, "ssh/dev/work")
			require.Error(t, err)
			require.Nil(t, windows, "never return partially trusted rows")
		})
	}
}

func TestSSHWindowInventoryBoundsAndFailures(t *testing.T) {
	transport := errors.New("transport failure")
	for _, test := range []struct {
		name   string
		result execwrap.CommandResult
		err    error
		match  error
	}{
		{"stdout overflow", execwrap.CommandResult{Stdout: []byte(strings.Repeat("x", (1<<20)+1))}, nil, execwrap.ErrOutputLimit},
		{"stderr overflow", execwrap.CommandResult{Stderr: []byte(strings.Repeat("x", (4<<10)+1))}, nil, execwrap.ErrOutputLimit},
		{"executor overflow", execwrap.CommandResult{Stdout: []byte(sshWindowTestOutput(sshWindowTestRows()))}, execwrap.ErrOutputLimit, execwrap.ErrOutputLimit},
		{"transport error", execwrap.CommandResult{Stdout: []byte(sshWindowTestOutput(sshWindowTestRows()))}, transport, transport},
		{"missing remote session", execwrap.CommandResult{ExitCode: 1, Stderr: []byte("remote tmux session not found")}, nil, nil},
		{"SSH failure", execwrap.CommandResult{ExitCode: 255, Stderr: []byte("unavailable host")}, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			sh := sshWindowTestShell(t, test.result, test.err)
			windows, err := ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, "ssh/dev/work")
			require.Error(t, err)
			require.Nil(t, windows)
			if test.match != nil {
				require.ErrorIs(t, err, test.match)
			}
			if len(test.result.Stderr) < 4096 {
				require.Contains(t, err.Error(), strings.TrimSpace(string(test.result.Stderr)))
			}
		})
	}
	for _, count := range []int{9998, 9999} {
		t.Run(fmt.Sprint("record count ", count), func(t *testing.T) {
			rows := sshWindowTestRows()
			for _, row := range rows {
				row[8], row[9] = "1:1:/s", strconv.Itoa(count)
			}
			var output strings.Builder
			output.WriteString(sshWindowTestRow(rows[0]))
			for i := range count {
				row := append([]string(nil), rows[1]...)
				row[3], row[4], row[5], row[6], row[7] = "@"+strconv.Itoa(i), strconv.Itoa(i), "0", "", ""
				if i == 0 {
					row[5] = "1"
				}
				output.WriteString(sshWindowTestRow(row))
			}
			output.WriteString(sshWindowTestRow(rows[2]))
			require.Less(t, output.Len(), 1<<20)
			sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(output.String())}, nil)
			windows, err := ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, "ssh/dev/work")
			if count == 9998 {
				require.NoError(t, err)
				require.Len(t, windows, count)
			} else {
				require.ErrorContains(t, err, "record limit")
				require.Nil(t, windows)
			}
		})
	}
}

func TestSSHWindowInventoryCancellationAndDeadline(t *testing.T) {
	config := model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}
	t.Run("cancelled before execution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		sh := shell.NewMockShell(t)
		windows, err := ListSSHWindows(ctx, config, sh, "ssh/dev/work")
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, windows)
		require.Empty(t, sh.Calls)
	})
	for _, mode := range []string{"cancel", "parent deadline", "five-second deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "parent deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
			}
			defer cancel()
			started := make(chan struct{})
			sh := shell.NewMockShell(t)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", 1<<20, 4<<10,
				"-T", "-n", "-o", "BatchMode=yes", "--", "dev", mock.Anything).
				RunAndReturn(func(child context.Context, _ string, _, _ int, _ ...string) (execwrap.CommandResult, error) {
					deadline, ok := child.Deadline()
					require.True(t, ok)
					require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
					close(started)
					<-child.Done()
					return execwrap.CommandResult{ExitCode: -1}, child.Err()
				}).Once()
			done := make(chan error, 1)
			go func() {
				windows, err := ListSSHWindows(ctx, config, sh, "ssh/dev/work")
				if windows != nil {
					err = errors.New("cancelled discovery returned rows")
				}
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("SSH did not start")
			}
			want := context.DeadlineExceeded
			if mode == "cancel" {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, want)
			case <-time.After(6 * time.Second):
				t.Fatal("SSH deadline not enforced")
			}
		})
	}
}
