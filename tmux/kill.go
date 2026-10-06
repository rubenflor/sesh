package tmux

import (
	"errors"
	"strconv"
	"strings"

	"github.com/joshmedeski/sesh/v2/shell"
)

// PID plus server startup time distinguishes practical PID reuse; socket_path
// also scopes inventory to the configured server. Read alongside session_id.
const serverGenerationFormat = "#{pid}:#{start_time}:#{socket_path}"

func validServerGeneration(value string) bool {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[2] == "" || parts[2] == "#{socket_path}" || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, part := range parts[:2] {
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil || number == 0 || strconv.FormatUint(number, 10) != part {
			return false
		}
	}
	return true
}

// KillSession kills the tmux session identified by target. The trailing colon
// forces session (not window) target resolution, so a session whose name looks
// like a window index still resolves to itself.
func (t *RealTmux) KillSession(target string) (string, error) {
	return t.shell.Cmd(t.bin, "kill-session", "-t", target+":")
}

// KillSessionInGeneration uses the verified session ID, guarded by its server
// lifetime in the same tmux command queue. A restart after live validation
// cannot turn that ID into authorization for a different server's session.
func (t *RealTmux) KillSessionInGeneration(sessionID, serverGeneration string) (string, error) {
	id, found := strings.CutPrefix(sessionID, "$")
	number, err := strconv.ParseUint(id, 10, 64)
	if !found || err != nil || strconv.FormatUint(number, 10) != id || !validServerGeneration(serverGeneration) {
		return "", errors.New("unverified tmux session identity")
	}
	// Escape tmux format literals independently of the command parser's shell
	// quoting: socket paths may contain commas, closing braces, or hashes.
	literal := strings.NewReplacer("#", "##", ",", "#,", "}", "#}").Replace(serverGeneration)
	condition := "#{==:" + serverGenerationFormat + ",#{l:" + literal + "}}"
	const changed = "sesh: tmux server generation changed"
	output, err := t.shell.CmdCapture(t.bin, "if-shell", "-F", "-t", sessionID+":", condition,
		"kill-session -t "+shell.Quote(sessionID+":"), "display-message -p "+shell.Quote(changed))
	if err != nil {
		return output, err
	}
	if output == changed {
		return "", errors.New(changed)
	}
	return output, nil
}
