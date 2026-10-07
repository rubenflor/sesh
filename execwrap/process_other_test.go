//go:build !unix

package execwrap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

const unsupportedSpawnMarkerEnv = "SESH_EXECWRAP_UNSUPPORTED_SPAWN_MARKER"

func TestUnsupportedProcessCleanupDoesNotSpawn(t *testing.T) {
	// Use an executable that would succeed if started, without running tests.
	command := exec.Command(os.Args[0], "-test.run=^$")
	cleanup, err := prepareProcessCleanup(command)
	if command.Process != nil {
		_ = command.Wait()
		t.Fatal("unsupported cleanup hook spawned a process")
	}
	if cleanup != nil || command.ProcessState != nil || err == nil || err.Error() != "process execution is unsupported on "+runtime.GOOS {
		t.Fatalf("unsupported cleanup = %v, %v, %v", cleanup == nil, command.ProcessState, err)
	}
}

func TestRunContextUnsupportedPlatform(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	t.Setenv(unsupportedSpawnMarkerEnv, marker)
	result, err := NewExec().RunContext(context.Background(), os.Args[0], "-test.run=^TestUnsupportedPlatformSpawnHelper$")
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsupported platform started helper: marker stat error = %v", statErr)
	}
	if err == nil || err.Error() != "process execution is unsupported on "+runtime.GOOS {
		t.Fatalf("unsupported RunContext error = %v", err)
	}
	if result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("unsupported RunContext executed the command: %+v", result)
	}
}

func TestUnsupportedPlatformSpawnHelper(t *testing.T) {
	marker := os.Getenv(unsupportedSpawnMarkerEnv)
	if marker == "" {
		return
	}
	if err := os.WriteFile(marker, []byte("started"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
