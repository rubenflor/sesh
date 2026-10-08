package tmux

import (
	"fmt"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
)

// windowTargetFormat makes tmux print the window it just created, so callers
// can hand a concrete target to a script or a later send-keys instead of
// guessing an index.
const windowTargetFormat = "#{session_name}:#{window_index}"

const createdPaneFormat = "#{session_id} #{window_id} #{pane_id} " + serverGenerationFormat

// NewWindowInSession creates a window and returns its "session:index" target.
func (t *RealTmux) NewWindowInSession(opts model.TmuxWindowOpts, serverGeneration ...string) (string, error) {
	target, err := t.sessionCommand(t.bin, opts.TargetSession, serverGeneration, newWindowArgs(opts, windowTargetFormat)...)
	return strings.TrimSpace(target), err
}

// NewWindowPaneInSession retains the initial pane's native creation identity.
// It does not rediscover the pane from the window's name, index or selection.
func (t *RealTmux) NewWindowPaneInSession(opts model.TmuxWindowOpts, serverGeneration ...string) (model.TmuxPane, error) {
	if !validTmuxID(opts.TargetSession, "$") {
		return model.TmuxPane{}, fmt.Errorf("unverified tmux session identity")
	}
	args, err := sessionCommandArgs(opts.TargetSession, serverGeneration, newWindowArgs(opts, createdPaneFormat)...)
	if err != nil {
		return model.TmuxPane{}, err
	}
	output, err := t.shell.CmdCapture(t.bin, append([]string{"-u"}, args...)...)
	if err != nil {
		return model.TmuxPane{}, err
	}
	fields := strings.SplitN(output, " ", 4)
	if len(fields) != 4 {
		return model.TmuxPane{}, fmt.Errorf("tmux returned unreadable created pane identity: %q", output)
	}
	pane := model.TmuxPane{SessionID: fields[0], WindowID: fields[1], PaneID: fields[2], ServerGeneration: fields[3]}
	_, identityErr := sessionIdentityCondition(pane.SessionID, pane.ServerGeneration)
	if identityErr != nil || !validWindowID(pane.WindowID) || !validTmuxID(pane.PaneID, "%") || pane.SessionID != opts.TargetSession || (len(serverGeneration) != 0 && pane.ServerGeneration != serverGeneration[0]) {
		return model.TmuxPane{}, fmt.Errorf("tmux returned unverified created pane identity: %q", output)
	}
	return pane, nil
}

func newWindowArgs(opts model.TmuxWindowOpts, format string) []string {
	args := []string{"new-window", "-P", "-F", format}
	if opts.Name != "" {
		args = append(args, "-n", opts.Name)
	}
	if opts.StartDir != "" {
		args = append(args, "-c", opts.StartDir)
	}
	if opts.Background {
		args = append(args, "-d")
	}
	if opts.TargetSession != "" {
		// trailing colon forces session (not window) target resolution
		args = append(args, "-t", opts.TargetSession+":")
	}
	if opts.Command != "" {
		args = append(args, "--", opts.Command)
	}
	return args
}
