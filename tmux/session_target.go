package tmux

import "fmt"

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
