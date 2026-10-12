package seshcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const sshWindowCLIInventory = `RW1::B::$7::"work"::::::::""::""::"101:1700000000:/tmp/remote"::1
RW1::W::$7::"work"::@9::0::1::"docker"::"/srv/project"::"101:1700000000:/tmp/remote"::1
RW1::E::$7::"work"::::::::""::""::"101:1700000000:/tmp/remote"::1
`

func runSSHWindowCommand(t *testing.T, ctx context.Context, sh shell.Shell, args ...string) (string, error) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "sesh.toml")
	require.NoError(t, os.WriteFile(config, []byte("cache = false\n[ssh]\nhosts = ['dev']\n"), 0600))
	base := NewBaseDeps()
	base.Shell = sh
	root := &cobra.Command{Use: "sesh", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().String("config", config, "")
	root.AddCommand(NewWindowCommand(base))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"window"}, args...))
	err := root.ExecuteContext(ctx)
	return stdout.String(), err
}

func TestCLIWindowListSSHJSON(t *testing.T) {
	sh := shell.NewMockShell(t)
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", 1<<20, 4<<10,
		"-T", "-n", "-o", "BatchMode=yes", "--", "dev", mock.Anything).
		Return(execwrap.CommandResult{Stdout: []byte(sshWindowCLIInventory)}, nil).Once()
	// The old local-only implementation reaches this instead of SSH.
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", 16<<20, 64<<10,
		"-u", "list-sessions", "-F", mock.Anything).
		Return(execwrap.CommandResult{}, nil).Maybe()

	output, err := runSSHWindowCommand(t, t.Context(), sh, "list", "--json", "--target", "ssh/dev/work")

	require.NoError(t, err)
	require.JSONEq(t, `[{"Name":"docker","Path":"/srv/project","Index":0,"Active":true,"Src":"ssh","Session":"ssh/dev/work","SessionID":"$7","ID":"@9","ServerFingerprint":"101:1700000000:/tmp/remote"}]`, output)
	require.Len(t, sh.Calls, 1, "remote listing must never consult local tmux or bridges")
}

func TestCLIWindowListSSHUsesStdoutByDefault(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput], func(t *testing.T) {
			sh := shell.NewMockShell(t)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", 1<<20, 4<<10,
				"-T", "-n", "-o", "BatchMode=yes", "--", "dev", mock.Anything).
				Return(execwrap.CommandResult{Stdout: []byte(sshWindowCLIInventory)}, nil).Once()
			config := filepath.Join(t.TempDir(), "sesh.toml")
			require.NoError(t, os.WriteFile(config, []byte("[ssh]\nhosts = ['dev']\n"), 0600))
			base := NewBaseDeps()
			base.Shell = sh
			cmd := NewWindowCommand(base)
			cmd.PersistentFlags().String("config", config, "")
			args := []string{"list", "--target", "ssh/dev/work"}
			if jsonOutput {
				args = append(args, "--json")
			}
			cmd.SetArgs(args)
			// No SetOut: normal CLI execution must put data on stdout, not stderr.
			output := captureWindowStdout(t, func() error { return cmd.ExecuteContext(t.Context()) })
			if jsonOutput {
				require.JSONEq(t, `[{"Name":"docker","Path":"/srv/project","Index":0,"Active":true,"Src":"ssh","Session":"ssh/dev/work","SessionID":"$7","ID":"@9","ServerFingerprint":"101:1700000000:/tmp/remote"}]`, output)
			} else {
				require.Equal(t, "docker\n", output)
			}
		})
	}
}

func TestCLIWindowListSSHTextEmptyAndErrors(t *testing.T) {
	for _, test := range []struct {
		name, inventory, want string
		json                  bool
		status                int
		err                   error
	}{
		{name: "text", inventory: sshWindowCLIInventory, want: "docker\n"},
		{name: "empty", json: true, inventory: `RW1::B::$7::"work"::::::::""::""::"101:1700000000:/tmp/remote"::0
RW1::E::$7::"work"::::::::""::""::"101:1700000000:/tmp/remote"::0
`, want: "[]\n"},
		{name: "malformed", inventory: "not an inventory\n"},
		{name: "partial SSH failure", inventory: sshWindowCLIInventory, status: 255},
		{name: "executor failure", err: errors.New("SSH unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			sh := shell.NewMockShell(t)
			sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", 1<<20, 4<<10,
				"-T", "-n", "-o", "BatchMode=yes", "--", "dev", mock.Anything).
				Return(execwrap.CommandResult{Stdout: []byte(test.inventory), Stderr: []byte("SSH diagnostic"), ExitCode: test.status}, test.err).Once()
			args := []string{"list", "--target", "ssh/dev/work"}
			if test.json {
				args = append(args, "--json")
			}
			output, err := runSSHWindowCommand(t, t.Context(), sh, args...)
			if test.want != "" {
				require.NoError(t, err)
				require.Equal(t, test.want, output)
			} else {
				require.Error(t, err)
				require.Empty(t, output)
			}
			require.Len(t, sh.Calls, 1, "never fall back after SSH claiming")
		})
	}
}

func TestCLIWindowListSSHInvalidTargetsFailClosed(t *testing.T) {
	for _, target := range []string{"ssh/dev", "ssh/dev/", "ssh/dev/a/b", "ssh/dev/%77ork", "ssh/dev/a%2fb", "ssh/other/work", "ssh/dev/%00"} {
		t.Run(target, func(t *testing.T) {
			sh := shell.NewMockShell(t)
			output, err := runSSHWindowCommand(t, t.Context(), sh, "list", "--json", "--target", target)
			require.Error(t, err)
			require.Empty(t, output)
			require.Empty(t, sh.Calls)
		})
	}
}

func TestCLIWindowListRejectsAggregateFlags(t *testing.T) {
	for _, flag := range []string{"--all", "--tmux", "--ssh"} {
		t.Run(flag, func(t *testing.T) {
			sh := shell.NewMockShell(t)
			output, err := runSSHWindowCommand(t, t.Context(), sh, "list", "--json", flag)
			require.ErrorContains(t, err, "unknown flag")
			require.Empty(t, output)
			require.Empty(t, sh.Calls)
		})
	}
}

func TestCLIWindowListSSHCancellation(t *testing.T) {
	sh := shell.NewMockShell(t)
	started := make(chan struct{})
	sh.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", 1<<20, 4<<10,
		"-T", "-n", "-o", "BatchMode=yes", "--", "dev", mock.Anything).
		RunAndReturn(func(ctx context.Context, _ string, _, _ int, _ ...string) (execwrap.CommandResult, error) {
			close(started)
			<-ctx.Done()
			return execwrap.CommandResult{ExitCode: -1}, ctx.Err()
		}).Once()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		output, err := runSSHWindowCommand(t, ctx, sh, "list", "--json", "--target", "ssh/dev/work")
		if output != "" {
			err = errors.New("cancellation printed rows")
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("SSH did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("CLI did not cancel discovery")
	}
}

func TestCLIWindowConnectSSHRemainsUnsupported(t *testing.T) {
	for _, target := range []string{"ssh/dev/work", "ssh/dev", "ssh/other/work"} {
		for _, flags := range [][]string{nil, {"--switch"}, {"--new", "--command", "touch MUST_NOT_RUN"}, {"--background", "--path", "/must/not/create"}} {
			t.Run(target+strings.Join(flags, " "), func(t *testing.T) {
				sh := shell.NewMockShell(t)
				args := append([]string{"connect", "--target", target}, flags...)
				output, err := runSSHWindowCommand(t, t.Context(), sh, append(args, "docker")...)
				require.Error(t, err)
				require.Empty(t, output)
				require.Empty(t, sh.Calls, "unsupported remote connection cannot create, select, attach or bridge anything")
			})
		}
	}
}

func TestCLIWindowListLocalJSONUnchanged(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "attached", true: "explicit"}[explicit], func(t *testing.T) {
			t.Setenv("TMUX", "/tmp/private-local,101,0")
			s := shell.NewMockShell(t)
			const generation = "101:1700000000:/tmp/local"
			row := sessionInventoryRow([]string{"100", "", "1", "", "100", "0", "", "0", "", "", "0", "0", "0", "$7", "100", "0", "0", "local", "/local", "", "1", generation})
			s.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", 16<<20, 64<<10, "-u", "list-sessions", "-F", mock.Anything).
				Return(execwrap.CommandResult{Stdout: []byte(row)}, nil)
			s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$7", "--", "SESH_SSH_IDENTITY").Return("", nil)
			if !explicit {
				s.EXPECT().Cmd("tmux", "display-message", "-p", "#{session_id}::#{pid}:#{start_time}:#{socket_path}").Return("$7::"+generation, nil).Once()
				s.EXPECT().Cmd("tmux", "display-message", "-p", "-t", "$7:", "#{session_id}::#{pid}:#{start_time}:#{socket_path}").Return("$7::"+generation, nil).Once()
			}
			s.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", 16<<20, 64<<10, "-u", "if-shell", "-F", "-t", "$7:", mock.Anything, mock.Anything, "if-shell").
				Return(execwrap.CommandResult{Stdout: []byte(`W1::$7::"local"::@9::4::1::"notes"::"/local"::"` + generation + `"`)}, nil).Once()
			args := []string{"list", "--json"}
			if explicit {
				args = append(args, "--target", "local")
			}
			output := captureWindowStdout(t, func() error { _, err := runSSHWindowCommand(t, t.Context(), s, args...); return err })
			require.JSONEq(t, `[{"Name":"notes","Path":"/local","Index":4,"Active":true}]`, output)
		})
	}
}
