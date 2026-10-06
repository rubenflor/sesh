package connector

import (
	"fmt"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
)

func tmuxinatorStrategy(c *RealConnector, name string) (model.Connection, error) {
	session, exists := c.lister.FindTmuxinatorConfig(name)
	if !exists {
		return model.Connection{Found: false}, nil
	}

	return model.Connection{
		Found:       true,
		Session:     session,
		New:         true,
		AddToZoxide: false,
	}, nil
}

func connectToTmuxinator(c *RealConnector, connection model.Connection, opts model.ConnectOpts) (string, error) {
	if _, err := c.tmuxinator.Start(connection.Session.Name); err != nil {
		return "", fmt.Errorf("failed to start tmuxinator session: %w", err)
	}
	sessionID, err := tmux.LocalSessionID(c.tmux, connection.Session.Name)
	if err != nil {
		return "", err
	}
	message, err := c.tmux.SwitchOrAttach(sessionID, opts)
	return strings.Replace(message, sessionID, connection.Session.Name, 1), err
}
