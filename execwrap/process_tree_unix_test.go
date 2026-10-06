//go:build unix

package execwrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestRunContextBoundsInheritedPipeCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
			}
			t.Cleanup(func() {
				cancel()
				for _, name := range []string{"parent", "descendant"} {
					if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
						pid, _ := strconv.Atoi(string(data))
						if pid > 0 {
							_ = syscall.Kill(pid, syscall.SIGKILL)
						}
					}
				}
			})
			done := make(chan runResult, 1)
			go func() {
				result, err := NewExec().RunContext(ctx, os.Args[0], "-test.run=^TestPipeTreeHelper$", "--", "parent", dir)
				done <- runResult{result, err}
			}()
			waitForHelper(t, filepath.Join(dir, "parent"), done)
			if mode == "cancel" {
				cancel()
			}
			select {
			case got := <-done:
				var exitErr *exec.ExitError
				if !errors.As(got.err, &exitErr) || exitErr.ProcessState == nil || got.result.ExitCode != -1 {
					t.Fatalf("want killed and reaped child, got %+v, %v", got.result, got.err)
				}
				if string(got.result.Stdout) != "partial stdout" || string(got.result.Stderr) != "partial stderr" {
					t.Fatalf("streams lost or combined: %+v", got.result)
				}
				data, err := os.ReadFile(filepath.Join(dir, "parent"))
				if err != nil {
					t.Fatal(err)
				}
				pid, _ := strconv.Atoi(string(data))
				if got := exitErr.ProcessState.Pid(); got != pid {
					t.Fatalf("waited child PID = %d, want recorded parent PID %d", got, pid)
				}
				data, err = os.ReadFile(filepath.Join(dir, "descendant"))
				if err != nil {
					t.Fatal(err)
				}
				pid, _ = strconv.Atoi(string(data))
				if err := syscall.Kill(pid, 0); err != nil {
					t.Fatalf("descendant exited before pipe-drain check: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation waited for descendant holding stdout/stderr")
			}
		})
	}
}

func TestPipeTreeHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	mode, dir := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestPipeTreeHelper$", "--", "descendant", dir)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "descendant")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprint(os.Stdout, "partial stdout")
		fmt.Fprint(os.Stderr, "partial stderr")
	}
	if err := os.WriteFile(filepath.Join(dir, mode), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		panic(err)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
