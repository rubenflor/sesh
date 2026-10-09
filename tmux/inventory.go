package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/joshmedeski/sesh/v2/execwrap"
)

const (
	maxInventoryBytes   = 16 << 20
	maxInventoryRecords = 10000
	maxInventoryStderr  = 64 << 10
	// Basic tmux q: escapes these bytes; control and UTF-8 bytes stay literal.
	inventoryEscapes = "|&;<>()$`\\\"'*?[# =%"
	// Basic q: on a composite expansion does not escape its nested socket_path.
	inventoryGenerationFormat = `"#{pid}:#{start_time}:#{q:socket_path}"`
)

func (t *RealTmux) captureInventory(args ...string) (string, error) {
	result, err := t.shell.CmdCaptureRaw(context.Background(), t.bin, maxInventoryBytes, maxInventoryStderr, args...)
	if err != nil {
		if emptyInventoryFailure(result, err) {
			return "", nil
		}
		if len(args) > 1 && args[1] == "list-panes" && inventoryExitOne(result, err) {
			message := strings.TrimSuffix(string(result.Stderr), "\n")
			if message == "no current target" || message == "no current session" {
				// A missing current target is empty only if session inventory proves it.
				probe, probeErr := t.captureInventory("-u", "list-sessions", "-F", listsessionsformat())
				if probeErr != nil {
					return "", errors.Join(err, fmt.Errorf("session inventory probe failed: %w", probeErr))
				}
				if probe == "" {
					return "", nil
				}
			}
		}
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("tmux inventory exited with status %d", result.ExitCode)
	}
	return string(result.Stdout), nil
}

func inventoryExitOne(result execwrap.CommandResult, err error) bool {
	var exitErr *exec.ExitError
	return len(result.Stdout) == 0 && result.ExitCode == 1 &&
		!errors.Is(err, execwrap.ErrOutputLimit) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func emptyInventoryFailure(result execwrap.CommandResult, err error) bool {
	if !inventoryExitOne(result, err) {
		return false
	}
	message := strings.TrimSuffix(string(result.Stderr), "\n")
	if strings.ContainsAny(message, "\r\n") {
		return false
	}
	if message == "no sessions" {
		return true
	}
	if socket, found := strings.CutPrefix(message, "no server running on "); found {
		return socket != ""
	}
	socket, found := strings.CutPrefix(message, "error connecting to ")
	if !found {
		return false
	}
	socket, found = strings.CutSuffix(socket, " (No such file or directory)")
	return found && socket != ""
}

type inventorySchema struct {
	tag    string
	fields []string
}

func (s inventorySchema) format() string {
	return s.tag + separator + strings.Join(s.fields, separator)
}

// decode consumes raw stdout, including at most one final record terminator.
// The complete quoted stream is decoded before callers validate native identity.
func (s inventorySchema) decode(output string) ([][]string, error) {
	if len(output) > maxInventoryBytes {
		return nil, errors.New("tmux inventory exceeds size limit")
	}
	records := make([][]string, 0)
	for output != "" {
		if len(records) == maxInventoryRecords {
			return nil, errors.New("tmux inventory exceeds record limit")
		}
		var found bool
		output, found = strings.CutPrefix(output, s.tag+separator)
		if !found {
			return nil, errors.New("unsupported tmux inventory schema")
		}
		fields := make([]string, len(s.fields))
		for i, format := range s.fields {
			if strings.HasPrefix(format, `"`) {
				var err error
				fields[i], output, err = takeQuotedInventoryField(output)
				if err != nil {
					return nil, err
				}
			} else {
				end := 0
				for end < len(output) && output[end] != ':' && output[end] != '\n' {
					if output[end] == '\x00' || output[end] == '\r' || output[end] == '"' || output[end] == '\\' {
						return nil, errors.New("malformed tmux inventory scalar")
					}
					end++
				}
				fields[i], output = output[:end], output[end:]
			}
			if i < len(fields)-1 {
				output, found = strings.CutPrefix(output, separator)
				if !found {
					return nil, errors.New("malformed tmux inventory field boundary")
				}
			}
		}
		if output != "" {
			output, found = strings.CutPrefix(output, "\n")
			if !found {
				return nil, errors.New("malformed tmux inventory record boundary")
			}
		}
		records = append(records, fields)
	}
	return records, nil
}

func takeQuotedInventoryField(output string) (string, string, error) {
	if len(output) == 0 || output[0] != '"' {
		return "", "", errors.New("missing tmux inventory quote")
	}
	var value strings.Builder
	start := 1
	for i := 1; i < len(output); i++ {
		switch output[i] {
		case '\x00':
			return "", "", errors.New("NUL in tmux inventory field")
		case '"':
			value.WriteString(output[start:i])
			return value.String(), output[i+1:], nil
		case '\\':
			if i+1 == len(output) || strings.IndexByte(inventoryEscapes, output[i+1]) < 0 {
				return "", "", errors.New("malformed tmux inventory escape")
			}
			value.WriteString(output[start:i])
			i++
			value.WriteByte(output[i])
			start = i + 1
		}
	}
	return "", "", errors.New("unterminated tmux inventory quote")
}
