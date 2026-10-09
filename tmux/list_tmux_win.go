package tmux

import (
	"errors"
	"strconv"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
)

var windowInventorySchema = inventorySchema{
	tag: "W1",
	fields: []string{
		"#{session_id}",
		`"#{q:session_name}"`,
		"#{window_id}",
		"#{window_index}",
		"#{window_active}",
		`"#{q:window_name}"`,
		`"#{q:pane_current_path}"`,
		inventoryGenerationFormat,
	},
}

func listWindowsFormat() string {
	return windowInventorySchema.format()
}

func (t *RealTmux) ListWindows(targetSession string, serverGeneration ...string) ([]*model.TmuxWindow, error) {
	var args []string
	args = append(args, "list-windows")
	if targetSession != "" {
		args = append(args, "-t", targetSession)
	}
	args = append(args, "-F", listWindowsFormat())
	args, err := sessionCommandArgs(targetSession, serverGeneration, args...)
	if err != nil {
		return nil, err
	}

	args = append([]string{"-u"}, args...)
	output, err := t.captureInventory(args...)
	if err != nil {
		return nil, err
	}
	// A verified live session always has a window; empty capture cannot satisfy its guard.
	if len(serverGeneration) != 0 && output == "" {
		return nil, errors.New("unavailable verified tmux window inventory")
	}
	return parseTmuxWindowsOutput(output)
}

func listAllWindowNamesFormat(format string) string {
	if format == "" {
		format = model.DefaultWindowNameFormat
	}
	variables := []string{
		"#{session_name}",
		format,
	}
	return strings.Join(variables, separator)
}

// ListAllWindowNames returns the formatted window names of every session,
// keyed by session name, in a single tmux invocation.
func (t *RealTmux) ListAllWindowNames(format string) (map[string][]string, error) {
	output, err := t.shell.ListCmd(t.bin, "list-windows", "-a", "-F", listAllWindowNamesFormat(format))
	if err != nil {
		return nil, err
	}
	return parseAllWindowNamesOutput(output), nil
}

// ListAllWindows returns the windows of every session, keyed by session name,
// in a single tmux invocation.
func (t *RealTmux) ListAllWindows() (map[string][]model.TmuxWindow, error) {
	output, err := t.captureInventory("-u", "list-windows", "-a", "-F", listWindowsFormat())
	if err != nil {
		return nil, err
	}
	return parseAllWindowsOutput(output)
}

func parseAllWindowsOutput(output string) (map[string][]model.TmuxWindow, error) {
	records, err := windowInventorySchema.decode(output)
	if err != nil {
		return nil, err
	}
	windows := make(map[string][]model.TmuxWindow)
	for _, fields := range records {
		window, err := parseTmuxWindow(fields)
		if err != nil {
			return nil, err
		}
		windows[fields[1]] = append(windows[fields[1]], *window)
	}
	return windows, nil
}

func parseAllWindowNamesOutput(rawList []string) map[string][]string {
	windowNames := make(map[string][]string)
	for _, line := range rawList {
		// Split on the first separator only: window names are more likely to
		// contain it than session names (which come from directory names).
		session, name, found := strings.Cut(line, separator)
		if !found {
			continue
		}
		if session == "" || name == "" {
			continue
		}
		windowNames[session] = append(windowNames[session], name)
	}
	return windowNames
}

func parseTmuxWindowsOutput(output string) ([]*model.TmuxWindow, error) {
	records, err := windowInventorySchema.decode(output)
	if err != nil {
		return nil, err
	}
	windows := make([]*model.TmuxWindow, 0, len(records))
	for _, fields := range records {
		window, err := parseTmuxWindow(fields)
		if err != nil {
			return nil, err
		}
		windows = append(windows, window)
	}
	return windows, nil
}

func parseTmuxWindow(fields []string) (*model.TmuxWindow, error) {
	if !validTmuxID(fields[0], "$") || fields[1] == "" || !validWindowID(fields[2]) {
		return nil, errors.New("unverified tmux window inventory identity")
	}
	index, err := strconv.Atoi(fields[3])
	if err != nil || index < 0 || strconv.Itoa(index) != fields[3] || (fields[4] != "0" && fields[4] != "1") {
		return nil, errors.New("malformed tmux window inventory")
	}
	if !validServerGeneration(fields[7]) {
		return nil, errors.New("unverified tmux window inventory generation")
	}
	return &model.TmuxWindow{
		ID: fields[2], Index: index, Active: fields[4] == "1",
		Name: fields[5], Path: fields[6], ServerGeneration: fields[7],
	}, nil
}
