package previewer

import (
	"context"
	"testing"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/icon"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/ls"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPreviewerSSHEmptyCaptureDoesNotFallThrough(t *testing.T) {
	identity := "ssh/myinventa/deploy"
	mockLister := new(lister.MockLister)
	mockTmux := new(tmux.MockTmux)
	mockIcon := new(icon.MockIcon)
	mockDir := new(dir.MockDir)
	mockHome := new(home.MockHome)
	mockLs := new(ls.MockLs)
	mockShell := shell.NewMockShell(t)

	mockIcon.EXPECT().RemoveIcon(identity).Return(identity).Once()
	mockShell.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", SSHPreviewBytes, SSHPreviewStderr,
		"-T", "-n", "-o", "BatchMode=yes", "--", "myinventa", mock.Anything).
		Return(execwrap.CommandResult{}, nil).Once()

	p := NewPreviewer(
		mockLister,
		mockTmux,
		mockIcon,
		mockDir,
		mockHome,
		mockLs,
		model.Config{SSH: model.SSHConfig{Hosts: []string{"myinventa"}}},
		mockShell,
	)

	output, err := p.(ContextPreviewer).PreviewContext(context.Background(), identity)

	require.NoError(t, err)
	require.Empty(t, output)
	mockLister.AssertNotCalled(t, "FindTmuxSession", identity)
	mockLister.AssertNotCalled(t, "FindConfigSession", identity)
	mockShell.AssertNotCalled(t, "ShellCmd", mock.Anything, mock.Anything)
	mockLs.AssertNotCalled(t, "ListDirectory", mock.Anything)
}
