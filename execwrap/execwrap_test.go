package execwrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const helperProcessMarker = "execwrap-helper"

type runResult struct {
	result CommandResult
	err    error
}

func TestRunContextCapturesArgvAndSeparateStreams(t *testing.T) {
	result, err := NewExec().RunContext(
		context.Background(),
		os.Args[0],
		helperProcessArgs("output", "argument with spaces", "literal;argument")...,
	)
	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("RunContext() exit code = %d, want 0", result.ExitCode)
	}
	if got, want := string(result.Stdout), "argument with spaces\x00literal;argument"; got != want {
		t.Errorf("RunContext() stdout = %q, want %q", got, want)
	}
	if got, want := string(result.Stderr), "notice"; got != want {
		t.Errorf("RunContext() stderr = %q, want %q", got, want)
	}
}

func TestRunContextReturnsNonZeroExitInformation(t *testing.T) {
	result, err := NewExec().RunContext(
		context.Background(),
		os.Args[0],
		helperProcessArgs("nonzero")...,
	)
	if err == nil {
		t.Fatal("RunContext() error = nil, want non-zero process error")
	}
	if result.ExitCode != 23 {
		t.Errorf("RunContext() exit code = %d, want 23", result.ExitCode)
	}
	if got, want := string(result.Stdout), "partial output"; got != want {
		t.Errorf("RunContext() stdout = %q, want %q", got, want)
	}
	if got, want := string(result.Stderr), "process failed"; got != want {
		t.Errorf("RunContext() stderr = %q, want %q", got, want)
	}
}

func TestRunContextStartFailureAndAlreadyCanceledContext(t *testing.T) {
	result, err := NewExec().RunContext(context.Background(), filepath.Join(t.TempDir(), "missing-command"))
	if err == nil || result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("start failure = %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = NewExec().RunContext(ctx, os.Args[0], helperProcessArgs("output")...)
	if !errors.Is(err, context.Canceled) || result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("already canceled command = %+v, %v", result, err)
	}
}

func TestRunContextCancellationTerminatesAndWaitsForChild(t *testing.T) {
	startedPath := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan runResult, 1)
	go func() {
		result, err := NewExec().RunContext(ctx, os.Args[0], helperProcessArgs("wait", startedPath)...)
		done <- runResult{result: result, err: err}
	}()

	waitForHelper(t, startedPath, done)
	cancel()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("RunContext() error = nil after cancellation, want error")
		}
		var exitErr *osexec.ExitError
		if !errors.As(got.err, &exitErr) || exitErr.ProcessState == nil {
			t.Fatalf("RunContext() cancellation error = %T %v, want waited process exit state", got.err, got.err)
		}
		if got.result.ExitCode == 0 {
			t.Errorf("RunContext() exit code = 0 after cancellation, want non-zero or signal status")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunContext() did not return after cancellation; child may not have terminated and been waited for")
	}
}

func TestExecwrapHelperProcess(t *testing.T) {
	marker := slices.Index(os.Args, helperProcessMarker)
	if marker == -1 {
		return
	}
	if marker+1 >= len(os.Args) {
		os.Exit(2)
	}

	switch os.Args[marker+1] {
	case "output":
		fmt.Fprint(os.Stdout, strings.Join(os.Args[marker+2:], "\x00"))
		fmt.Fprint(os.Stderr, "notice")
		os.Exit(0)
	case "nonzero":
		fmt.Fprint(os.Stdout, "partial output")
		fmt.Fprint(os.Stderr, "process failed")
		os.Exit(23)
	case "wait":
		if marker+2 >= len(os.Args) {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Args[marker+2], []byte("started"), 0o600); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(2)
	}
}

func helperProcessArgs(mode string, args ...string) []string {
	return append([]string{"-test.run=^TestExecwrapHelperProcess$", helperProcessMarker, mode}, args...)
}

func waitForHelper(t *testing.T, startedPath string, done <-chan runResult) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	for {
		if _, err := os.Stat(startedPath); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat helper readiness file: %v", err)
		}

		select {
		case got := <-done:
			t.Fatalf("helper returned before becoming ready: result=%+v err=%v", got.result, got.err)
		case <-timeout.C:
			t.Fatal("helper did not become ready")
		case <-ticker.C:
		}
	}
}
