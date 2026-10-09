package picker

import (
	"context"

	"github.com/joshmedeski/sesh/v2/previewer"
)

const (
	sshPreviewTimeout = previewer.SSHPreviewTimeout
	sshPreviewBytes   = previewer.SSHPreviewBytes
	sshPreviewStderr  = previewer.SSHPreviewStderr
)

func (p *RealPicker) previewSSHSession(ctx context.Context, identity string) (string, error) {
	return previewer.PreviewSSHSession(ctx, p.config, p.shell, identity)
}
