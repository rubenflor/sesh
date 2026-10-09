package picker

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
)

const (
	sshPreviewTimeout = 5 * time.Second
	sshPreviewBytes   = 64 * 1024
	sshPreviewStderr  = 4 * 1024
)

func (p *RealPicker) previewSSHSession(ctx context.Context, identity string) (string, error) {
	host, session, err := model.ParseSSHIdentity(identity)
	if err != nil {
		return "", err
	}
	if !slices.Contains(p.config.SSH.Hosts, host) {
		return "", fmt.Errorf("SSH host %q is not configured in [ssh].hosts", host)
	}
	if strings.ContainsRune(host, '\x00') || strings.ContainsRune(session, '\x00') {
		return "", fmt.Errorf("invalid SSH preview identity %q: NUL byte", identity)
	}
	ctx, cancel := context.WithTimeout(ctx, sshPreviewTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Only native IDs become targets; arbitrary names are compared as whole strings.
	remote := "wanted=" + shell.Quote(model.TmuxSSHSessionTarget(session)) + `
set -f
fail() { printf '%s\n' "$1" >&2; exit 1; }
ids=$(tmux -u list-sessions -F '#{session_id}') || exit $?
target=
for id in $ids; do
    case "$id" in '$'*) ;; *) fail 'invalid remote tmux session ID' ;; esac
    case "${id#\$}" in ''|*[!0-9]*) fail 'invalid remote tmux session ID' ;; esac
    # The suffix preserves trailing newlines through command substitution.
    name=$(tmux -u display-message -p -t "${id}:" '#{session_name}x') || exit $?
    if [ "$name" = "${wanted}x" ]; then
        [ -z "$target" ] || fail 'remote tmux session is ambiguous'
        target=$id
    fi
done
[ -n "$target" ] || fail 'remote tmux session not found'
exec tmux capture-pane -p -t "${target}:"
`
	result, err := p.shell.CmdCaptureRaw(ctx, "ssh", sshPreviewBytes, sshPreviewStderr,
		"-T", "-n", "-o", "BatchMode=yes", "--", host,
		"/bin/sh -c "+shell.Quote(remote))
	if ctx.Err() != nil {
		return "", fmt.Errorf("SSH %q: %w", host, ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("SSH %q: %w", host, err)
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(string(result.Stderr))
		if message == "" {
			message = "remote tmux command failed"
		}
		return "", fmt.Errorf("SSH %q: %s (status %d)", host, message, result.ExitCode)
	}
	return string(result.Stdout), nil
}
