package execwrap

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"
)

// CommandResult contains the output and exit status from a completed command.
type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int // -1 when the process did not exit normally or could not start
}

type ExecCmd interface {
	CombinedOutput() ([]byte, error)
	Output() ([]byte, error)
}

type Exec interface {
	LookPath(executable string) (string, error)
	Command(name string, args ...string) ExecCmd
	// RunContext returns separate output streams and exit status, and waits for
	// the direct child to exit even when ctx is cancelled.
	RunContext(ctx context.Context, name string, args ...string) (CommandResult, error)
}

type OsExec struct{}

func NewExec() Exec {
	return &OsExec{}
}

func (e *OsExec) LookPath(executable string) (string, error) {
	return exec.LookPath(executable)
}

func (e *OsExec) Command(name string, args ...string) ExecCmd {
	return exec.Command(name, args...)
}

// RunContext executes name with args using ctx, capturing stdout and stderr
// separately. The returned error is non-nil for start failures and non-zero
// process exits; ExitCode is -1 when no normal exit code is available.
// After cancellation or direct-child exit, inherited output pipes have at most
// 250 ms to drain. Wait still reaps the direct child before returning.
// On Unix, cancellation and completion also kill the owned process group so
// descendants cannot outlive discovery while retaining inherited pipes.
// Platforms without Unix process-group cleanup are rejected before spawning.
func (e *OsExec) RunContext(ctx context.Context, name string, args ...string) (CommandResult, error) {
	var stdout, stderr bytes.Buffer
	exitCode, err := runContext(ctx, name, &stdout, &stderr, args...)
	return CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode}, err
}

func runContext(ctx context.Context, name string, stdout, stderr io.Writer, args ...string) (int, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = 250 * time.Millisecond
	cleanup, err := prepareProcessCleanup(command)
	if err != nil {
		return -1, err
	}
	defer cleanup()
	command.Stdout = stdout
	command.Stderr = stderr

	err = command.Run()
	exitCode := -1
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	return exitCode, err
}
