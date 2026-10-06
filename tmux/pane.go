package tmux

import (
	"fmt"
	"strings"

	"github.com/joshmedeski/sesh/v2/convert"
	"github.com/joshmedeski/sesh/v2/model"
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
		fields := strings.Split(line, separator)
		if len(fields) != 9 {
			continue
		}
		panes = append(panes, &model.TmuxPane{
			WindowIndex: convert.StringToInt(fields[0]),
			WindowName:  fields[1],
			PaneIndex:   convert.StringToInt(fields[2]),
			PaneTitle:   fields[3],
			PaneCommand: fields[4],
			PanePath:    fields[5],
			PaneID:      fields[6],
			SessionID:   fields[7],
			WindowID:    fields[8],
		})
	}
	return panes, nil
}

func (t *RealTmux) SelectPane(pane *model.TmuxPane) (string, error) {
	if pane.SessionID == "" || pane.WindowID == "" || pane.PaneID == "" {
		return "", fmt.Errorf("pane has no server-assigned session, window or pane ID")
	}

	// Keep the verified owning session when selecting its window; display names
	// and numeric indexes are not operation targets.
	if _, err := t.shell.Cmd(t.bin, "select-window", "-t", pane.SessionID+":"+pane.WindowID); err != nil {
		return "", fmt.Errorf("failed to select window %d: %w", pane.WindowIndex, err)
	}
	if _, err := t.shell.Cmd(t.bin, "select-pane", "-t", pane.PaneID); err != nil {
		return "", fmt.Errorf("failed to select pane %d in window %d: %w", pane.PaneIndex, pane.WindowIndex, err)
	}
	return fmt.Sprintf("selected pane %d in window %d", pane.PaneIndex, pane.WindowIndex), nil
}

func (t *RealTmux) GetCurrentSession() (string, error) {
	return t.shell.Cmd(t.bin, "display-message", "-p", "#{session_name}")
}
