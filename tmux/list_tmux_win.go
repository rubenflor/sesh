package tmux

import (
	"errors"
	"strconv"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
)

func listWindowsFormat() string {
	variables := []string{
		"#{window_id}",
		"#{window_index}",
		"#{window_active}",
		"#{n:window_name}",
		"#{n:pane_current_path}",
		"#{n:" + serverGenerationFormat + "}",
		"#{window_name}#{pane_current_path}" + serverGenerationFormat,
	}
	// Only native IDs and decimal numbers precede the payload. tmux's n:
	// modifier counts bytes, so arbitrary text (including :: and newlines)
	// cannot shift an identity field or impersonate another inventory record.
	return strings.Join(variables, separator)
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

	// Keep the output bytes identical to n: lengths, even outside a UTF-8 locale.
	args = append([]string{"-u"}, args...)
	output, err := t.shell.ListCmd(t.bin, args...)
	if err != nil {
		return nil, err
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
	output, err := t.shell.ListCmd(t.bin, "-u", "list-windows", "-a", "-F", "#{n:session_name}"+separator+"#{session_name}"+listWindowsFormat())
	if err != nil {
		return nil, err
	}
	return parseAllWindowsOutput(output)
}

func parseAllWindowsOutput(rawList []string) (map[string][]model.TmuxWindow, error) {
	windows := make(map[string][]model.TmuxWindow)
	for output := strings.Join(rawList, "\n"); output != ""; {
		length, rest, found := strings.Cut(output, separator)
		if !found {
			return nil, errors.New("malformed tmux window inventory")
		}
		session, rest, err := takeWindowInventoryField(rest, length)
		if err != nil || session == "" {
			return nil, errors.New("malformed tmux window session inventory")
		}
		window, remaining, err := parseTmuxWindow(rest)
		if err != nil {
			return nil, err
		}
		windows[session] = append(windows[session], *window)
		output = remaining
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

func parseTmuxWindowsOutput(rawList []string) ([]*model.TmuxWindow, error) {
	windows := make([]*model.TmuxWindow, 0, len(rawList))
	// ListCmd splits on newlines. Rejoin before consuming byte-framed fields,
	// because a newline in display/path data is part of its record's payload.
	for output := strings.Join(rawList, "\n"); output != ""; {
		window, remaining, err := parseTmuxWindow(output)
		if err != nil {
			return nil, err
		}
		windows = append(windows, window)
		output = remaining
	}
	return windows, nil
}

func parseTmuxWindow(output string) (*model.TmuxWindow, string, error) {
	fields := strings.SplitN(output, separator, 7)
	if len(fields) != 7 || !validWindowID(fields[0]) {
		return nil, "", errors.New("unverified tmux window inventory identity")
	}
	index, err := strconv.Atoi(fields[1])
	if err != nil || index < 0 || strconv.Itoa(index) != fields[1] || (fields[2] != "0" && fields[2] != "1") {
		return nil, "", errors.New("malformed tmux window inventory")
	}
	payload := fields[6]
	var values [3]string
	for i := range values {
		values[i], payload, err = takeWindowInventoryField(payload, fields[3+i])
		if err != nil {
			return nil, "", err
		}
	}
	if !validServerGeneration(values[2]) {
		return nil, "", errors.New("unverified tmux window inventory generation")
	}
	if payload != "" {
		var found bool
		payload, found = strings.CutPrefix(payload, "\n")
		if !found {
			return nil, "", errors.New("malformed tmux window inventory record boundary")
		}
	}
	return &model.TmuxWindow{
		ID: fields[0], Index: index, Active: fields[2] == "1",
		Name: values[0], Path: values[1], ServerGeneration: values[2],
	}, payload, nil
}

func takeWindowInventoryField(payload, length string) (string, string, error) {
	size, err := strconv.Atoi(length)
	if err != nil || size < 0 || strconv.Itoa(size) != length || size > len(payload) {
		return "", "", errors.New("malformed tmux window inventory field length")
	}
	return payload[:size], payload[size:], nil
}
