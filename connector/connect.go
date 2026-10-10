package connector

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
)

// TODO: make it configurable to change the order of connection establishments?
// ["tmux", "config", "dir", "zoxide"]
// TODO: make it configurable to disable certain strategies (including flags for optimized fzf commands)
// sesh connect --config (sesh list --config | fzf)
var strategies = []func(*RealConnector, string) (model.Connection, error){
	sshStrategy,
	tmuxPaneStrategy,
	tmuxStrategy,
	tmuxinatorStrategy,
	configStrategy,
	configWildcardStrategy,
	dirStrategy,
	zoxideStrategy,
}

// Resolve runs the strategy chain to work out what a name refers to, without
// touching tmux. Window and pane connects share it so they identify a session
// exactly the way `sesh connect` does.
func (c *RealConnector) Resolve(name string) (model.Connection, error) {
	if !strings.HasPrefix(name, "ssh/") {
		name = c.resolveAlias(name)
	}
	for _, strategy := range strategies {
		connection, err := strategy(c, name)
		if err != nil {
			return model.Connection{}, fmt.Errorf("failed to establish connection: %w", err)
		}
		if connection.Found {
			return connection, nil
		}
	}
	return model.Connection{Found: false}, nil
}

// ConnectSession consumes picker rows without resolving a local display name.
// Discovered directories retain their literal path; other sources stay name-based.
func (c *RealConnector) ConnectSession(session model.SeshSession, opts model.ConnectOpts) (string, error) {
	if session.Src == "dir" && session.Path != "" {
		if !filepath.IsAbs(session.Path) {
			return "", fmt.Errorf("selected directory has no absolute path")
		}
		connection, err := directoryConnection(c, session.Path)
		if err != nil {
			return "", err
		}
		if !connection.Found {
			return "", fmt.Errorf("directory not found: %s", session.Path)
		}
		c.zoxide.Add(connection.Session.Path)
		return connectToTmux(c, connection, opts)
	}
	if session.Src == "tmux-pane" {
		return connectToTmuxPane(c, model.Connection{Session: session}, opts)
	}
	if session.Src != "tmux" {
		return c.Connect(session.Name, opts)
	}
	c.zoxide.Add(session.Path)
	return connectToTmux(c, model.Connection{Session: session}, opts)
}

// TODO: send to logging (local txt file?)
func (c *RealConnector) Connect(name string, opts model.ConnectOpts) (string, error) {
	connectStrategy := map[string]func(c *RealConnector, connection model.Connection, opts model.ConnectOpts) (string, error){
		"ssh":             connectToSSH,
		"tmux-pane":       connectToTmuxPane,
		"tmux":            connectToTmux,
		"tmuxinator":      connectToTmuxinator,
		"config":          connectToTmux,
		"config_wildcard": connectToTmux,
		"dir":             connectToTmux,
		"zoxide":          connectToTmux,
	}

	connection, err := c.Resolve(name)
	if err != nil {
		return "", err
	}
	if !connection.Found {
		return "", fmt.Errorf("no connection found for '%s'", name)
	}
	connect, exists := connectStrategy[connection.Session.Src]
	if !exists {
		return "", fmt.Errorf("no connector for source %q", connection.Session.Src)
	}

	// TODO: allow CLI flag to disable zoxide and overwrite all settings?
	// sesh connect --ignore-zoxide "dotfiles"
	if connection.AddToZoxide {
		c.zoxide.Add(connection.Session.Path)
	}
	return connect(c, connection, opts)
}
