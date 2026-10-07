package tmux

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
)

// LocalSessionID resolves a literal session name to the verified server target.
// Names are never passed back to tmux for target interpretation after this check.
func LocalSessionID(t Tmux, name string) (string, error) {
	sessions, err := t.ListSessions()
	if err != nil {
		return "", err
	}
	for _, session := range sessions {
		if session.Name == name && session.SSHBridgeIdentity == "" {
			if session.ID == "" {
				return "", fmt.Errorf("session %q has no server-assigned ID", name)
			}
			return session.ID, nil
		}
	}
	return "", fmt.Errorf("session %q not found", name)
}

// VerifyLocalSession revalidates an existing selection without consulting its
// display name. Metadata returned here belongs to that same ID and lifetime.
func VerifyLocalSession(t Tmux, sessionID, serverGeneration string) (model.TmuxSession, error) {
	if _, err := sessionIdentityCondition(sessionID, serverGeneration); err != nil {
		return model.TmuxSession{}, err
	}
	sessions, err := t.ListSessions()
	if err != nil {
		return model.TmuxSession{}, err
	}
	for _, session := range sessions {
		if session.ID == sessionID && session.ServerGeneration == serverGeneration && session.SSHBridgeIdentity == "" {
			return *session, nil
		}
	}
	return model.TmuxSession{}, fmt.Errorf("local tmux session %q changed or is unavailable", sessionID)
}

func sessionIdentityCondition(sessionID, serverGeneration string) (string, error) {
	digits, found := strings.CutPrefix(sessionID, "$")
	number, err := strconv.ParseUint(digits, 10, 64)
	if !found || err != nil || strconv.FormatUint(number, 10) != digits || !validServerGeneration(serverGeneration) {
		return "", errors.New("unverified tmux session identity")
	}
	literal := strings.NewReplacer("#", "##", ",", "#,", "}", "#}").Replace(serverGeneration)
	return "#{&&:#{==:#{session_id}," + sessionID + "},#{==:" + serverGenerationFormat + ",#{l:" + literal + "}}}", nil
}

// sessionCommandArgs guards the original session ID and generation in the
// operation's own tmux queue. Window indexes remain unchanged by this guard.
func sessionCommandArgs(target string, generation []string, args ...string) ([]string, error) {
	if len(generation) == 0 {
		return args, nil
	}
	if len(generation) != 1 {
		return nil, errors.New("unverified tmux session identity")
	}
	id, _, _ := strings.Cut(target, ":")
	condition, err := sessionIdentityCondition(id, generation[0])
	if err != nil {
		return nil, err
	}
	command := make([]string, len(args))
	for i, arg := range args {
		command[i] = shell.Quote(arg)
	}
	// tmux has no standalone error command. A missing required argument to
	// if-shell makes rejection fail even when attach streams terminal output.
	return []string{"if-shell", "-F", "-t", id + ":", condition, strings.Join(command, " "), "if-shell"}, nil
}

func (t *RealTmux) sessionCommand(bin, target string, generation []string, args ...string) (string, error) {
	guarded, err := sessionCommandArgs(target, generation, args...)
	if err != nil {
		return "", err
	}
	if len(generation) != 0 {
		// Cmd normalizes missing-server errors to empty success; a verified
		// operation must retain those errors instead.
		return t.shell.CmdCapture(bin, guarded...)
	}
	return t.shell.Cmd(bin, guarded...)
}
