//go:build unix

package execwrap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBoundedCaptureStopsAndReapsOverflowProcessTree(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result, err := RunContextBounded(ctx, os.Args[0], 256, 256,
				"-test.run=^TestBoundedCaptureTreeHelper$", "bounded-capture-helper", "parent", stream, dir)
			if !errors.Is(err, ErrOutputLimit) || len(result.Stdout) > 256 || len(result.Stderr) > 256 {
				t.Fatalf("overflow result=%+v, error=%v", result, err)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ProcessState == nil {
				t.Fatalf("child was not killed and waited for: %v", err)
			}
			parent, readErr := os.ReadFile(filepath.Join(dir, "parent"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid, _ := strconv.Atoi(string(parent))
			if exitErr.ProcessState.Pid() != pid || !exitErr.ProcessState.Exited() && result.ExitCode != -1 {
				t.Fatalf("wrong waited process: %+v", result)
			}
			assertTreeProcessGone(t, pid)
			descendant, readErr := os.ReadFile(filepath.Join(dir, "descendant"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid, _ = strconv.Atoi(string(descendant))
			assertTreeProcessGone(t, pid)
		})
	}
}

func TestBoundedCaptureTreeHelper(t *testing.T) {
	i := slices.Index(os.Args, "bounded-capture-helper")
	if i < 0 {
		return
	}
	mode, stream, dir := os.Args[i+1], os.Args[i+2], os.Args[i+3]
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestBoundedCaptureTreeHelper$", "bounded-capture-helper", "descendant", stream, dir)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(98)
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "descendant")); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, mode), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(98)
	}
	if mode == "parent" {
		output := os.Stdout
		if stream == "stderr" {
			output = os.Stderr
		}
		_, _ = output.WriteString(strings.Repeat("x", 257))
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
