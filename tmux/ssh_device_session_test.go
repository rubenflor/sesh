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

const sshDeviceSessionTestInventory = `DG1::B::O::$7::"work"::"views"::::::2::"101:1700000000:/tmp/remote"
DG1::B::D::$8::"device"::"views"::::::2::"101:1700000000:/tmp/remote"
DG1::W::O::$7::"work"::"views"::@9::0::2::"101:1700000000:/tmp/remote"
DG1::W::O::$7::"work"::"views"::@10::3::2::"101:1700000000:/tmp/remote"
DG1::W::D::$8::"device"::"views"::@9::0::2::"101:1700000000:/tmp/remote"
DG1::W::D::$8::"device"::"views"::@10::3::2::"101:1700000000:/tmp/remote"
DG1::E::O::$7::"work"::"views"::::::2::"101:1700000000:/tmp/remote"
DG1::E::D::$8::"device"::"views"::::::2::"101:1700000000:/tmp/remote"
`

func sshDeviceSessionTestConfig() (model.Config, model.SSHDeviceSession) {
	mapping := model.SSHDeviceSession{Original: "ssh/dev/work", Session: "ssh/dev/device"}
	return model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}, DeviceSessions: []model.SSHDeviceSession{mapping}}}, mapping
}

func TestSSHDeviceSessionRelation(t *testing.T) {
	config, mapping := sshDeviceSessionTestConfig()
	sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(sshDeviceSessionTestInventory)}, nil)

	relation, err := ValidateSSHDeviceSession(t.Context(), config, sh, mapping)

	require.NoError(t, err)
	require.Equal(t, SSHDeviceSessionRelation{
		Original: mapping.Original, Session: mapping.Session, OriginalID: "$7", SessionID: "$8",
		Group: "views", Links: []SSHDeviceWindowLink{{ID: "@9", Index: 0}, {ID: "@10", Index: 3}},
		ServerFingerprint: "101:1700000000:/tmp/remote",
	}, relation)
}

func TestSSHDeviceSessionRejectsUnconfiguredOrInvalidMapping(t *testing.T) {
	for _, test := range []struct{ original, session, host string }{
		{"ssh/dev/work", "ssh/dev/device", "other"},
		{"local", "ssh/dev/device", "dev"},
		{"ssh/dev/work", "ssh/other/device", "dev"},
		{"ssh/dev/work", "ssh/dev/work", "dev"},
		{"ssh/dev/%77ork", "ssh/dev/device", "dev"},
		{"ssh/dev/%00", "ssh/dev/device", "dev"},
	} {
		t.Run(test.original+"-"+test.host, func(t *testing.T) {
			mapping := model.SSHDeviceSession{Original: test.original, Session: test.session}
			sh := shell.NewMockShell(t)
			relation, err := ValidateSSHDeviceSession(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{test.host}, DeviceSessions: []model.SSHDeviceSession{mapping}}}, sh, mapping)
			require.Error(t, err)
			require.Zero(t, relation)
			require.Empty(t, sh.Calls, "local validation must precede SSH execution")
		})
	}
	config, mapping := sshDeviceSessionTestConfig()
	config.SSH.DeviceSessions = nil
	sh := shell.NewMockShell(t)
	relation, err := ValidateSSHDeviceSession(t.Context(), config, sh, mapping)
	require.ErrorContains(t, err, "not configured")
	require.Zero(t, relation)
	require.Empty(t, sh.Calls)
}

func TestSSHDeviceSessionRejectsIncompleteOrUnrelatedInventory(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(string) string
	}{
		{"empty", func(string) string { return "" }},
		{"truncated", func(s string) string { return strings.TrimSuffix(s, "\n") }},
		{"missing end", func(s string) string { return s[:strings.LastIndex(s[:len(s)-1], "\n")+1] }},
		{"missing original boundary", func(s string) string { _, tail, _ := strings.Cut(s, "\n"); return tail }},
		{"unsupported tag", func(s string) string { return strings.Replace(s, "DG1", "DG2", 1) }},
		{"unexpected record", func(s string) string { return s + "DG1::junk\n" }},
		{"banner", func(s string) string { return "unrequested output\n" + s }},
		{"unterminated quote", func(s string) string { return strings.Replace(s, `"work"`, `"work`, 1) }},
		{"bad escape", func(s string) string { return strings.Replace(s, `"work"`, `"\z"`, 1) }},
		{"NUL", func(s string) string { return strings.Replace(s, `"work"`, "\"wo\x00rk\"", 1) }},
		{"invalid UTF-8", func(s string) string { return "\xff" + s }},
		{"ungrouped", func(s string) string { return strings.ReplaceAll(s, `"views"`, `""`) }},
		{"different groups", func(s string) string { return strings.ReplaceAll(s, `"device"::"views"`, `"device"::"other"`) }},
		{"wrong literal original", func(s string) string { return strings.ReplaceAll(s, `"work"`, `"work-old"`) }},
		{"wrong literal device", func(s string) string { return strings.ReplaceAll(s, `"device"`, `"device-old"`) }},
		{"same session ID", func(s string) string { return strings.ReplaceAll(s, "$8", "$7") }},
		{"invalid session ID", func(s string) string { return strings.ReplaceAll(s, "$8", "$08") }},
		{"unverified fingerprint", func(s string) string { return strings.ReplaceAll(s, "101:1700000000:/tmp/remote", "101:0:/tmp/remote") }},
		{"different remote server", func(s string) string {
			return strings.ReplaceAll(s, "::\"device\"::\"views\"::::::2::\"101:", "::\"device\"::\"views\"::::::2::\"102:")
		}},
		{"group changed", func(s string) string {
			return strings.Replace(s, `DG1::E::D::$8::"device"::"views"`, `DG1::E::D::$8::"device"::"other"`, 1)
		}},
		{"count changed", func(s string) string {
			return strings.Replace(s, `DG1::E::D::$8::"device"::"views"::::::2`, `DG1::E::D::$8::"device"::"views"::::::3`, 1)
		}},
		{"noncanonical count", func(s string) string { return strings.ReplaceAll(s, `::2::"101:`, `::02::"101:`) }},
		{"overflowing count", func(s string) string { return strings.ReplaceAll(s, `::2::"101:`, `::999999999999999999999::"101:`) }},
		{"missing link", func(s string) string {
			return strings.Replace(s, "DG1::W::D::$8::\"device\"::\"views\"::@10::3::2::\"101:1700000000:/tmp/remote\"\n", "", 1)
		}},
		{"metadata payload", func(s string) string { return strings.Replace(s, `"views"::::::2`, `"views"::@9::::2`, 1) }},
		{"foreign window parent", func(s string) string { return strings.Replace(s, "DG1::W::D::$8", "DG1::W::D::$9", 1) }},
		{"wrong row role", func(s string) string { return strings.Replace(s, "DG1::W::D", "DG1::W::O", 1) }},
		{"wrong row group", func(s string) string { return strings.Replace(s, `"device"::"views"::@9`, `"device"::"other"::@9`, 1) }},
		{"wrong row count", func(s string) string { return strings.Replace(s, `@9::0::2`, `@9::0::3`, 1) }},
		{"invalid window ID", func(s string) string { return strings.Replace(s, "@9", "@09", 1) }},
		{"negative index", func(s string) string { return strings.Replace(s, "@9::0", "@9::-1", 1) }},
		{"noncanonical index", func(s string) string { return strings.Replace(s, "@9::0", "@9::00", 1) }},
		{"duplicate index", func(s string) string { return strings.Replace(s, "@10::3", "@10::0", 1) }},
		{"different window ID", func(s string) string { return strings.Replace(s, `"device"::"views"::@9`, `"device"::"views"::@99`, 1) }},
		{"different link index", func(s string) string {
			return strings.Replace(s, `"device"::"views"::@9::0`, `"device"::"views"::@9::1`, 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, mapping := sshDeviceSessionTestConfig()
			sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(test.edit(sshDeviceSessionTestInventory))}, nil)
			relation, err := ValidateSSHDeviceSession(t.Context(), config, sh, mapping)
			require.Error(t, err)
			require.Zero(t, relation, "never return partial or unverified metadata")
		})
	}
}

func TestSSHDeviceSessionRepeatedLinksAndLiteralMetadata(t *testing.T) {
	config, mapping := sshDeviceSessionTestConfig()
	mapping.Original = "ssh/dev/a%2Fb%25%20caf%C3%A9%E9%9B%AA%0A%5Cn"
	mapping.Session = "ssh/dev/-device%3A%20%24HOME"
	config.SSH.DeviceSessions[0] = mapping
	output := strings.ReplaceAll(sshDeviceSessionTestInventory, "@10", "@9")
	output = strings.ReplaceAll(output, `"work"`, quoteInventoryField("a/b% café雪\\n\\\\n"))
	output = strings.ReplaceAll(output, `"device"`, quoteInventoryField("-device: $HOME"))
	output = strings.ReplaceAll(output, `"views"`, quoteInventoryField("group ' \" /%\\n café雪::\nDG1::E\r\t"))
	sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(output), Stderr: []byte("nonfatal warning\n")}, nil)
	relation, err := ValidateSSHDeviceSession(t.Context(), config, sh, mapping)
	require.NoError(t, err)
	require.Equal(t, []SSHDeviceWindowLink{{ID: "@9", Index: 0}, {ID: "@9", Index: 3}}, relation.Links)
	require.Equal(t, mapping.Original, relation.Original)
	require.Equal(t, mapping.Session, relation.Session)
	require.Equal(t, "group ' \" /%\\n café雪::\nDG1::E\r\t", relation.Group)
}

func TestSSHDeviceSessionLimitsAndFailures(t *testing.T) {
	transport := errors.New("SSH transport failure")
	for _, test := range []struct {
		name      string
		result    execwrap.CommandResult
		err, want error
	}{
		{"stdout overflow", execwrap.CommandResult{Stdout: []byte(strings.Repeat("x", (1<<20)+1))}, nil, execwrap.ErrOutputLimit},
		{"stderr overflow", execwrap.CommandResult{Stderr: []byte(strings.Repeat("x", (4<<10)+1))}, nil, execwrap.ErrOutputLimit},
		{"executor overflow", execwrap.CommandResult{}, execwrap.ErrOutputLimit, execwrap.ErrOutputLimit},
		{"transport failure", execwrap.CommandResult{}, transport, transport},
		{"missing session", execwrap.CommandResult{ExitCode: 1, Stderr: []byte("remote tmux session not found")}, nil, nil},
		{"SSH failure", execwrap.CommandResult{ExitCode: 255, Stderr: []byte("unavailable host")}, nil, nil},
		{"silent failure", execwrap.CommandResult{ExitCode: 1}, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, mapping := sshDeviceSessionTestConfig()
			sh := sshWindowTestShell(t, test.result, test.err)
			relation, err := ValidateSSHDeviceSession(t.Context(), config, sh, mapping)
			require.Error(t, err)
			require.Zero(t, relation)
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
			} else {
				require.Contains(t, err.Error(), "SSH")
				require.Contains(t, err.Error(), strings.TrimSpace(string(test.result.Stderr)))
			}
		})
	}
	for _, count := range []int{4998, 4999} {
		t.Run(fmt.Sprint("links per member ", count), func(t *testing.T) {
			var output strings.Builder
			for _, kind := range []string{"B", "W", "E"} {
				for i, role := range []string{"O", "D"} {
					name := []string{"work", "device"}[i]
					if kind != "W" {
						fmt.Fprintf(&output, "DG1::%s::%s::$%d::\"%s\"::\"g\"::::::%d::\"1:1:/s\"\n", kind, role, 7+i, name, count)
						continue
					}
					for index := range count {
						fmt.Fprintf(&output, "DG1::W::%s::$%d::\"%s\"::\"g\"::@%d::%s::%d::\"1:1:/s\"\n", role, 7+i, name, index, strconv.Itoa(index), count)
					}
				}
			}
			require.Less(t, output.Len(), 1<<20)
			config, mapping := sshDeviceSessionTestConfig()
			sh := sshWindowTestShell(t, execwrap.CommandResult{Stdout: []byte(output.String())}, nil)
			relation, err := ValidateSSHDeviceSession(t.Context(), config, sh, mapping)
			if count == 4998 {
				require.NoError(t, err)
				require.Len(t, relation.Links, count)
			} else {
				require.Error(t, err)
				require.Zero(t, relation)
			}
		})
	}
}

func TestSSHDeviceSessionCancellationAndOverallDeadline(t *testing.T) {
	config, mapping := sshDeviceSessionTestConfig()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	sh := shell.NewMockShell(t)
	relation, err := ValidateSSHDeviceSession(ctx, config, sh, mapping)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, relation)
	require.Empty(t, sh.Calls)

	for _, useParentDeadline := range []bool{false, true} {
		t.Run(fmt.Sprint("parent deadline ", useParentDeadline), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if useParentDeadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
			}
			defer cancel()
			sh := shell.NewMockShell(t)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", 1<<20, 4<<10, "-T", "-n", "-o", "BatchMode=yes", "--", "dev", mock.Anything).
				RunAndReturn(func(inner context.Context, _ string, _, _ int, _ ...string) (execwrap.CommandResult, error) {
					deadline, ok := inner.Deadline()
					require.True(t, ok)
					require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
					if !useParentDeadline {
						require.Greater(t, time.Until(deadline), 4*time.Second, "the overall deadline is five seconds, not a shorter per-command timeout")
						cancel()
					}
					<-inner.Done()
					return execwrap.CommandResult{Stdout: []byte(sshDeviceSessionTestInventory)}, inner.Err()
				}).Once()
			relation, err := ValidateSSHDeviceSession(ctx, config, sh, mapping)
			want := context.Canceled
			if useParentDeadline {
				want = context.DeadlineExceeded
			}
			require.ErrorIs(t, err, want)
			require.Zero(t, relation)
		})
	}
}
