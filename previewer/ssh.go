package previewer

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
	SSHPreviewTimeout = 5 * time.Second
	SSHPreviewBytes   = 64 * 1024
	SSHPreviewStderr  = 4 * 1024
)

// SSHPreviewStrategy previews a configured remote tmux session without using
// the local bridge/attach path.
type SSHPreviewStrategy struct {
	config model.Config
	shell  shell.Shell
}

func NewSSHStrategy(config model.Config, sh shell.Shell) *SSHPreviewStrategy {
	return &SSHPreviewStrategy{config: config, shell: sh}
}

func (s *SSHPreviewStrategy) Handles(name string) bool {
	return strings.HasPrefix(name, "ssh/")
}

func (s *SSHPreviewStrategy) Execute(name string) (string, error) {
	return s.ExecuteContext(context.Background(), name)
}

func (s *SSHPreviewStrategy) ExecuteContext(ctx context.Context, name string) (string, error) {
	if !s.Handles(name) {
		return "", nil
	}
	return PreviewSSHSession(ctx, s.config, s.shell, name)
}

// PreviewSSHSession captures the active pane of an exact remote tmux session.
func PreviewSSHSession(ctx context.Context, config model.Config, sh shell.Shell, identity string) (string, error) {
	host, session, err := model.ParseSSHIdentity(identity)
	if err != nil {
		return "", err
	}
	if !slices.Contains(config.SSH.Hosts, host) {
		return "", fmt.Errorf("SSH host %q is not configured in [ssh].hosts", host)
	}
	if strings.ContainsRune(host, '\x00') || strings.ContainsRune(session, '\x00') {
		return "", fmt.Errorf("invalid SSH preview identity %q: NUL byte", identity)
	}
	ctx, cancel := context.WithTimeout(ctx, SSHPreviewTimeout)
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
    case "$id" in '$'*) ;; *) fail 'invalid remote tmux session ID' ;;
    esac
    case "${id#\$}" in ''|*[!0-9]*) fail 'invalid remote tmux session ID' ;;
    esac
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
	result, err := sh.CmdCaptureRaw(ctx, "ssh", SSHPreviewBytes, SSHPreviewStderr,
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
