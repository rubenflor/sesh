package execwrap

import (
	"context"
	"errors"
	"fmt"
)

// ErrOutputLimit identifies overflow of either stream during bounded capture.
var ErrOutputLimit = errors.New("command output exceeds limit")

// RunContextBounded preserves raw streams and RunContext's process cleanup.
// Overflow cancels the owned process group; partial bytes are diagnostic only.
func RunContextBounded(ctx context.Context, name string, stdoutLimit, stderrLimit int, args ...string) (CommandResult, error) {
	if stdoutLimit < 0 || stderrLimit < 0 {
		return CommandResult{ExitCode: -1}, errors.New("negative command output limit")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := boundedCapture{limit: stdoutLimit, stream: "stdout", cancel: cancel}
	stderr := boundedCapture{limit: stderrLimit, stream: "stderr", cancel: cancel}
	exitCode, err := runContext(ctx, name, &stdout, &stderr, args...)
	return CommandResult{Stdout: stdout.data, Stderr: stderr.data, ExitCode: exitCode}, errors.Join(stdout.err, stderr.err, err, ctx.Err())
}

type boundedCapture struct {
	data   []byte
	limit  int
	stream string
	cancel context.CancelFunc
	err    error
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	n := min(len(p), b.limit-len(b.data))
	if len(b.data)+n > cap(b.data) {
		// Clamp capacity as well as length, without overflowing the growth sum.
		capacity := min(b.limit, max(len(b.data)+n, 4096, cap(b.data)+min(cap(b.data), b.limit-cap(b.data))))
		data := make([]byte, len(b.data), capacity)
		copy(data, b.data)
		b.data = data
	}
	b.data = append(b.data, p[:n]...)
	if n != len(p) {
		b.err = fmt.Errorf("%w: %s exceeds %d bytes", ErrOutputLimit, b.stream, b.limit)
		b.cancel()
		return n, b.err
	}
	return n, nil
}
