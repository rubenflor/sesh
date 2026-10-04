package seshcli

import (
	"context"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNewBaseDepsWithExecInjectsRunner(t *testing.T) {
	fakeExec := new(execwrap.MockExec)
	base := NewBaseDepsWithExec(fakeExec)
	assert.Same(t, fakeExec, base.Exec)

	want := execwrap.CommandResult{
		Stdout:   []byte("captured"),
		Stderr:   []byte("diagnostic"),
		ExitCode: 0,
	}
	fakeExec.On("RunContext", mock.Anything, "ssh", "host alias").Return(want, nil).Once()
	got, err := base.Exec.RunContext(context.Background(), "ssh", "host alias")
	require.NoError(t, err)
	assert.Equal(t, want, got)

	fakeCommand := new(execwrap.MockExecCmd)
	fakeExec.On("Command", "probe", "argument").Return(fakeCommand).Once()
	fakeCommand.On("Output").Return([]byte("shell output"), nil).Once()
	output, err := base.Shell.ListCmd("probe", "argument")
	require.NoError(t, err)
	assert.Equal(t, []string{"shell output"}, output)

	fakeExec.AssertExpectations(t)
	fakeCommand.AssertExpectations(t)
}
