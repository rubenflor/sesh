package execwrap

import (
	"bytes"
	"context"
	"os/exec"
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
func (e *OsExec) RunContext(ctx context.Context, name string, args ...string) (CommandResult, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	result := CommandResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: -1,
	}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	return result, err
}
