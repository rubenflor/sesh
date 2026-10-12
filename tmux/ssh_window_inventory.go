package tmux

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
)

const (
	sshWindowTimeout = 5 * time.Second
	sshWindowBytes   = 1 << 20
	sshWindowStderr  = 4 << 10
)

// The existing quoted codec carries W1 fields, plus record kind and window count.
var sshWindowSchema = inventorySchema{
	tag:    "RW1",
	fields: append(append([]string{"W"}, windowInventorySchema.fields...), "#{session_windows}"),
}

func sshWindowMetadataFormat(kind string) string {
	return (inventorySchema{tag: "RW1", fields: []string{
		kind, "#{session_id}", `"#{q:session_name}"`, "", "", "", `""`, `""`,
		inventoryGenerationFormat, "#{session_windows}",
	}}).format()
}

// ListSSHWindows enumerates one configured parent without attaching or bridging.
// Native enumeration can still invoke user-defined tmux after/error hooks.
func ListSSHWindows(ctx context.Context, config model.Config, sh shell.Shell, identity string) ([]model.SSHWindow, error) {
	host, session, err := model.ParseSSHIdentity(identity)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(config.SSH.Hosts, host) {
		return nil, fmt.Errorf("SSH host %q is not configured in [ssh].hosts", host)
	}
	if strings.ContainsRune(host, '\x00') || strings.ContainsRune(session, '\x00') {
		return nil, errors.New("invalid SSH window identity: NUL byte")
	}
	ctx, cancel := context.WithTimeout(ctx, sshWindowTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remote := sshSessionLookupScript(session) + `
tmux -N -u display-message -p -t "${target}:" ` + shell.Quote(sshWindowMetadataFormat("B")) + ` || exit $?
tmux -N -u list-windows -t "$target" -F ` + shell.Quote(sshWindowSchema.format()) + ` || exit $?
exec tmux -N -u display-message -p -t "${target}:" ` + shell.Quote(sshWindowMetadataFormat("E")) + "\n"
	output, err := captureSSHInventory(ctx, sh, host, remote, "remote tmux window command failed")
	if err != nil {
		return nil, err
	}
	windows, err := parseSSHWindows(string(output), identity, session)
	if err != nil {
		return nil, fmt.Errorf("SSH %q: invalid window inventory: %w", host, err)
	}
	return windows, nil
}

// Compare stored vis spelling literally; only verified native IDs become targets.
func sshSessionLookupScript(session string) string {
	return "wanted=" + shell.Quote(model.TmuxSSHSessionTarget(session)) + `
set -f
fail() { printf '%s\n' "$1" >&2; exit 1; }
ids=$(tmux -N -u list-sessions -F '#{session_id}') || exit $?
target=
count=0
for id in $ids; do
    count=$((count + 1))
    [ "$count" -le 10000 ] || fail 'remote tmux session inventory exceeds limit'
    case "$id" in '$'*) ;; *) fail 'invalid remote tmux session ID' ;;
    esac
    case "${id#\$}" in ''|*[!0-9]*) fail 'invalid remote tmux session ID' ;;
    esac
    name=$(tmux -N -u display-message -p -t "${id}:" '#{session_id}:#{session_name}x') || exit $?
    if [ "$name" = "${id}:${wanted}x" ]; then
        [ -z "$target" ] || fail 'remote tmux session is ambiguous'
        target=$id
    fi
done
[ -n "$target" ] || fail 'remote tmux session not found'
`
}

func captureSSHInventory(ctx context.Context, sh shell.Shell, host, remote, failure string) ([]byte, error) {
	result, err := sh.CmdCaptureRaw(ctx, "ssh", sshWindowBytes, sshWindowStderr,
		"-T", "-n", "-o", "BatchMode=yes", "--", host, "/bin/sh -c "+shell.Quote(remote))
	if ctx.Err() != nil {
		return nil, fmt.Errorf("SSH %q: %w", host, ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("SSH %q: %w", host, err)
	}
	if len(result.Stdout) > sshWindowBytes || len(result.Stderr) > sshWindowStderr {
		return nil, fmt.Errorf("SSH %q: %w", host, execwrap.ErrOutputLimit)
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(string(result.Stderr))
		if message == "" {
			message = failure
		}
		return nil, fmt.Errorf("SSH %q: %s (status %d)", host, message, result.ExitCode)
	}
	return result.Stdout, nil
}

func parseSSHWindows(output, identity, session string) ([]model.SSHWindow, error) {
	if !utf8.ValidString(output) || !strings.HasSuffix(output, "\n") {
		return nil, errors.New("invalid UTF-8 or incomplete inventory")
	}
	records, err := sshWindowSchema.decode(output)
	if err != nil {
		return nil, err
	}
	if len(records) < 2 || records[0][0] != "B" || records[len(records)-1][0] != "E" {
		return nil, errors.New("missing inventory boundary")
	}
	begin, end := records[0], records[len(records)-1]
	if !slices.Equal(begin[1:], end[1:]) || !validTmuxID(begin[1], "$") ||
		begin[2] != model.TmuxSSHSessionTarget(session) || !validServerGeneration(begin[8]) {
		return nil, errors.New("remote parent or server changed")
	}
	for _, field := range begin[3:8] {
		if field != "" {
			return nil, errors.New("unexpected inventory metadata")
		}
	}
	count, err := strconv.Atoi(begin[9])
	if err != nil || count < 0 || strconv.Itoa(count) != begin[9] || count != len(records)-2 {
		return nil, errors.New("incomplete window inventory")
	}
	windows := make([]model.SSHWindow, 0, count)
	indices := make(map[int]bool, count)
	active := 0
	for _, fields := range records[1 : len(records)-1] {
		if fields[0] != "W" || fields[1] != begin[1] || fields[2] != begin[2] || fields[8] != begin[8] || fields[9] != begin[9] {
			return nil, errors.New("unexpected parent, server or record")
		}
		window, err := parseTmuxWindow(fields[1:9])
		if err != nil {
			return nil, err
		}
		// A window can have multiple links in one session, each at a distinct index.
		if indices[window.Index] {
			return nil, errors.New("duplicate window index")
		}
		indices[window.Index] = true
		if window.Active {
			active++
		}
		windows = append(windows, model.SSHWindow{
			Name: window.Name, Path: window.Path, Index: window.Index, Active: window.Active,
			Src: "ssh", Session: identity, SessionID: begin[1], ID: window.ID,
			ServerFingerprint: begin[8],
		})
	}
	if count > 0 && active != 1 {
		return nil, errors.New("ambiguous active window state")
	}
	return windows, nil
}
