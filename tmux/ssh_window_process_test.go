//go:build unix

package tmux_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

func TestSSHWindowProcessCleanup(t *testing.T) {
	testSSHInventoryProcessCleanup(t, func(ctx context.Context, sh shell.Shell) error {
		windows, err := tmux.ListSSHWindows(ctx, model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, "ssh/dev/work")
		if windows != nil {
			return errors.New("failed process returned authoritative windows")
		}
		return err
	})
}

func testSSHInventoryProcessCleanup(t *testing.T, observe func(context.Context, shell.Shell) error) {
	t.Helper()
	for _, mode := range []string{"connecting cancel", "inventory cancel", "parent deadline", "stdout overflow", "stderr overflow"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nexec " + shell.Quote(os.Args[0]) + " -test.run='^TestSSHWindowProcessHelper$' -- " + shell.Quote(mode) + " " + shell.Quote(dir) + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700))
			t.Setenv("PATH", dir+":/usr/bin:/bin")
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "parent deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 4*time.Second)
			}
			t.Cleanup(func() {
				cancel()
				for _, file := range []string{"parent", "child"} {
					if data, err := os.ReadFile(filepath.Join(dir, file)); err == nil {
						pid, _ := strconv.Atoi(string(data))
						if pid > 0 && pid != os.Getpid() {
							_ = syscall.Kill(pid, syscall.SIGKILL)
						}
					}
				}
			})
			done := make(chan error, 1)
			go func() {
				done <- observe(ctx, shell.NewShell(execwrap.NewExec(), nil))
			}()
			require.Eventually(t, func() bool {
				for _, file := range []string{"parent", "child"} {
					data, err := os.ReadFile(filepath.Join(dir, file))
					pid, parseErr := strconv.Atoi(string(data))
					if err != nil || parseErr != nil || pid <= 0 {
						return false
					}
				}
				return true
			}, 3*time.Second, 10*time.Millisecond, "race-instrumented SSH helpers must publish complete PID records before cancellation")
			want := execwrap.ErrOutputLimit
			if strings.HasSuffix(mode, "cancel") {
				cancel()
				want = context.Canceled
			}
			if mode == "parent deadline" {
				want = context.DeadlineExceeded
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, want)
				data, readErr := os.ReadFile(filepath.Join(dir, "parent"))
				require.NoError(t, readErr)
				parent, parseErr := strconv.Atoi(string(data))
				require.NoError(t, parseErr)
				require.ErrorIs(t, syscall.Kill(parent, 0), syscall.ESRCH, "the direct child must already be reaped when discovery returns")
				data, readErr = os.ReadFile(filepath.Join(dir, "child"))
				require.NoError(t, readErr)
				child, parseErr := strconv.Atoi(string(data))
				require.NoError(t, parseErr)
				require.Eventually(t, func() bool {
					if errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
						return true
					}
					status, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(child)).Output()
					return strings.HasPrefix(strings.TrimSpace(string(status)), "Z")
				}, 3*time.Second, 10*time.Millisecond, "no running orphan may retain the capture pipes")
			case <-time.After(6 * time.Second):
				t.Fatal("fake SSH cancellation/overflow did not complete")
			}
		})
	}
}

func TestSSHWindowProcessHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	mode, dir := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if mode != "child" {
		child := exec.Command(os.Args[0], "-test.run=^TestSSHWindowProcessHelper$", "--", "child", dir)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(dir, "child")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				panic("child did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	file := "parent"
	if mode == "child" {
		file = "child"
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		panic(err)
	}
	switch mode {
	case "stdout overflow":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), (1<<20)+4096))
	case "stderr overflow":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), (4<<10)+4096))
	case "inventory cancel", "parent deadline":
		fmt.Fprint(os.Stdout, "RW1::B::$7::")
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
