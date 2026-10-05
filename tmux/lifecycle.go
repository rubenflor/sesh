package tmux

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
)

// The initial pane process waits at a tmux gate until its options are set.
// The signal is retained even if the process has not reached wait-for yet.
func (t *RealTmux) newSessionWithOptions(args []string, opts model.TmuxSessionOpts) (string, error) {
	if opts.Command == "" {
		return "", fmt.Errorf("tmux creation options require an initial command")
	}
	gate := "sesh-start-" + rand.Text()
	command := shell.Quote(t.bin) + " wait-for " + shell.Quote(gate) + " && " + opts.Command
	output, err := t.shell.CmdCapture(t.bin, append(args, "-P", "-F", "#{session_id} #{pane_id}", "--", command)...)
	if err != nil {
		return "", fmt.Errorf("tmux cannot create a gated session pane: %w", err)
	}
	ids := strings.Fields(output)
	if len(ids) != 2 || !strings.HasPrefix(ids[0], "$") || !strings.HasPrefix(ids[1], "%") {
		// Successful new-session (without -A) created this exact name. Clean up
		// even if a compatible executable cannot report its server-assigned IDs.
		_, cleanupErr := t.KillSession("=" + opts.Name)
		return "", errors.Join(fmt.Errorf("tmux returned unreadable session/pane IDs: %q", output), cleanupErr)
	}
	cleanup := func(cause error) (string, error) {
		_, cleanupErr := t.KillSession(ids[0])
		return "", errors.Join(cause, cleanupErr)
	}
	for _, option := range opts.SessionOptions {
		if _, err := t.shell.CmdCapture(t.bin, "set-option", "-t", ids[0], option.Name, option.Value); err != nil {
			return cleanup(fmt.Errorf("tmux does not support required session option %q=%q: %w", option.Name, option.Value, err))
		}
	}
	for _, option := range opts.PaneOptions {
		if _, err := t.shell.CmdCapture(t.bin, "set-option", "-p", "-t", ids[1], option.Name, option.Value); err != nil {
			return cleanup(fmt.Errorf("tmux does not support required pane option %q=%q: %w", option.Name, option.Value, err))
		}
	}
	if _, err := t.shell.CmdCapture(t.bin, "wait-for", "-S", gate); err != nil {
		return cleanup(fmt.Errorf("failed to start tmux pane command: %w", err))
	}
	return output, nil
}

func (t *RealTmux) IsPaneDead(targetSession string) (bool, error) {
	output, err := t.shell.CmdCapture(t.bin, "display-message", "-p", "-t", targetSession+":", "#{pane_dead}")
	if err != nil {
		return false, err
	}
	switch output {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("tmux returned unreadable pane state: %q", output)
	}
}
