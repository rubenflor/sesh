package tmux

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joshmedeski/sesh/v2/convert"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
)

func listpanesformat() string {
	variables := []string{
		"#{window_index}",
		"#{window_name}",
		"#{pane_index}",
		"#{pane_title}",
		"#{pane_current_command}",
		"#{pane_current_path}",
		"#{pane_id}",
		"#{session_id}",
		"#{window_id}",
		serverGenerationFormat,
	}
	return strings.Join(variables, separator)
}

func (t *RealTmux) ListTmuxPanes() ([]*model.TmuxPane, error) {
	output, err := t.shell.ListCmd(t.bin, "list-panes", "-s", "-F", listpanesformat())
	if err != nil {
		return []*model.TmuxPane{}, nil
	}
	panes, err := parseTmuxPanesOutput(output)
	if err != nil {
		return nil, err
	}
	markers := make(map[string]string)
	for _, pane := range panes {
		marker, found := markers[pane.SessionID]
		if !found {
			marker, err = t.GetSessionEnvironment(pane.SessionID, model.SSHBridgeIdentityEnv)
			if err != nil {
				return nil, fmt.Errorf("failed to read SSH bridge marker for pane %q in session %q: %w", pane.PaneID, pane.SessionID, err)
			}
			markers[pane.SessionID] = marker
		}
		pane.SSHBridgeIdentity = marker
	}
	return panes, nil
}

func parseTmuxPanesOutput(rawList []string) ([]*model.TmuxPane, error) {
	panes := make([]*model.TmuxPane, 0, len(rawList))
	for _, line := range rawList {
		fields := strings.SplitN(line, separator, 10)
		if len(fields) != 9 && len(fields) != 10 {
			continue
		}
		pane := &model.TmuxPane{
			WindowIndex: convert.StringToInt(fields[0]),
			WindowName:  fields[1],
			PaneIndex:   convert.StringToInt(fields[2]),
			PaneTitle:   fields[3],
			PaneCommand: fields[4],
			PanePath:    fields[5],
			PaneID:      fields[6],
			SessionID:   fields[7],
			WindowID:    fields[8],
		}
		if len(fields) == 10 && validServerGeneration(fields[9]) {
			pane.ServerGeneration = fields[9]
		}
		panes = append(panes, pane)
	}
	return panes, nil
}

func (t *RealTmux) SelectPane(pane *model.TmuxPane) (string, error) {
	if pane == nil || pane.SessionID == "" || !strings.HasPrefix(pane.WindowID, "@") || !strings.HasPrefix(pane.PaneID, "%") {
		return "", fmt.Errorf("pane has no server-assigned session, window or pane ID")
	}
	condition, err := sessionIdentityCondition(pane.SessionID, pane.ServerGeneration)
	if err != nil {
		return "", err
	}
	// if-shell permits an unresolved target. Strict pane selection must validate
	// the full tuple before changing window focus; an error skips window selection.
	windowTarget := pane.SessionID + ":" + pane.WindowID
	paneTarget := windowTarget + "." + pane.PaneID
	command := "select-pane -t " + shell.Quote(paneTarget) + " ; select-window -t " + shell.Quote(windowTarget)
	if _, err := t.shell.CmdCapture(t.bin, "if-shell", "-F", "-t", paneTarget, condition, command, "if-shell"); err != nil {
		return "", fmt.Errorf("failed to select pane %d in window %d: %w", pane.PaneIndex, pane.WindowIndex, err)
	}
	return fmt.Sprintf("selected pane %d in window %d", pane.PaneIndex, pane.WindowIndex), nil
}

// GetCurrentSession observes the current server identity, then verifies that
// exact ID is still live in the same generation after reading its ownership.
func (t *RealTmux) GetCurrentSession() (model.TmuxSession, error) {
	current, err := t.sessionIdentity("")
	if err != nil {
		return model.TmuxSession{}, err
	}
	current.SSHBridgeIdentity, err = t.GetSessionEnvironment(current.ID, model.SSHBridgeIdentityEnv)
	if err != nil {
		return model.TmuxSession{}, fmt.Errorf("couldn't check current tmux session ownership: %w", err)
	}
	live, err := t.sessionIdentity(current.ID)
	if err != nil {
		return model.TmuxSession{}, err
	}
	if live.ID != current.ID || live.ServerGeneration != current.ServerGeneration {
		return model.TmuxSession{}, fmt.Errorf("current tmux session identity changed")
	}
	return current, nil
}

func (t *RealTmux) sessionIdentity(target string) (model.TmuxSession, error) {
	args := []string{"display-message", "-p"}
	if target != "" {
		args = append(args, "-t", target+":")
	}
	output, err := t.shell.Cmd(t.bin, append(args, "#{session_id}"+separator+serverGenerationFormat)...)
	if err != nil {
		return model.TmuxSession{}, err
	}
	id, generation, found := strings.Cut(output, separator)
	digits, hasPrefix := strings.CutPrefix(id, "$")
	number, idErr := strconv.ParseUint(digits, 10, 64)
	if !found || !hasPrefix || idErr != nil || strconv.FormatUint(number, 10) != digits || !validServerGeneration(generation) {
		return model.TmuxSession{}, fmt.Errorf("current tmux session has no valid server-assigned identity")
	}
	return model.TmuxSession{ID: id, ServerGeneration: generation}, nil
}
