package connector

import (
	"fmt"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
)

func isTmuxPaneFormat(name string) bool {
	return strings.Contains(name, "/") && !strings.HasPrefix(name, "/")
}

func tmuxPaneStrategy(c *RealConnector, name string) (model.Connection, error) {
	if !c.tmux.IsAttached() || !isTmuxPaneFormat(name) {
		return model.Connection{Found: false}, nil
	}

	sessions, err := c.lister.ListTmuxPanes()
	if err != nil {
		return model.Connection{Found: false}, nil
	}

	for _, key := range sessions.OrderedIndex {
		session := sessions.Directory[key]
		if session.Name == name {
			return model.Connection{
				Found:       true,
				Session:     session,
				New:         false,
				AddToZoxide: false,
			}, nil
		}
	}

	return model.Connection{Found: false}, nil
}

func connectToTmuxPane(c *RealConnector, connection model.Connection, _ model.ConnectOpts) (string, error) {
	selected := connection.Session
	if selected.TmuxWindowID == "" || selected.TmuxPaneID == "" {
		return "", fmt.Errorf("pane %q has no server-assigned window or pane ID", selected.Name)
	}
	if _, err := tmux.VerifyLocalSession(c.tmux, selected.TmuxSessionID, selected.TmuxServerGeneration); err != nil {
		return "", err
	}
	panes, err := c.tmux.ListTmuxPanes()
	if err != nil {
		return "", err
	}
	for _, pane := range panes {
		if pane.SessionID == selected.TmuxSessionID && pane.WindowID == selected.TmuxWindowID &&
			pane.PaneID == selected.TmuxPaneID && pane.ServerGeneration == selected.TmuxServerGeneration && pane.SSHBridgeIdentity == "" {
			return c.tmux.SelectPane(pane)
		}
	}

	return "", fmt.Errorf("pane not found: %s", connection.Session.Name)
}
