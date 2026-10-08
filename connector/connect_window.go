package connector

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
)

// ConnectWindow selects the named window in the target session, creating it
// when it isn't there. It returns the window's public "session:index" label.
//
// An unnamed window has nothing to match on, so it is always created. So is one
// asked for with opts.New, which allows windows to share a name; a later
// connect to that name reuses the lowest-indexed one.
func (c *RealConnector) ConnectWindow(opts model.WindowConnectOpts) (string, error) {
	session, sessionID, err := c.targetSession(opts.Session)
	if err != nil {
		return "", err
	}
	var generation []string
	if session.TmuxSessionID != "" {
		generation = []string{session.TmuxServerGeneration}
	}

	name, startDir := opts.Name, opts.Path
	if startDir == "" {
		// A directory argument names the window after its basename and roots
		// it there, so `sesh window ~/c/sesh` opens a window called sesh. The
		// name is derived before matching, so running it twice selects the
		// window the first run created instead of opening a duplicate.
		if dirName, dirPath, ok := c.windowDir(name); ok {
			name, startDir = dirName, dirPath
		} else {
			startDir = session.Path
		}
	}

	if name != "" && !opts.New {
		windows, err := c.tmux.ListWindows(sessionID, generation...)
		if err != nil {
			return "", fmt.Errorf("failed to list windows in '%s': %w", session.Name, err)
		}
		for _, window := range windows {
			if window.Name != name {
				continue
			}
			if !strings.HasPrefix(window.ID, "@") {
				return "", fmt.Errorf("window %q has no server-assigned ID", name)
			}
			if window.ServerGeneration == "" || (len(generation) != 0 && window.ServerGeneration != generation[0]) {
				return "", fmt.Errorf("window %q has a missing or changed server generation", name)
			}
			if len(generation) == 0 {
				// A newly started session can already have configured windows.
				// Bind reuse to the lifetime that supplied this existing window.
				generation = []string{window.ServerGeneration}
			}
			target := sessionID + ":" + window.ID
			if _, err := c.reuseWindow(target, opts, generation...); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s:%d", session.Name, window.Index), nil
		}
	}

	target, err := c.tmux.NewWindowInSession(model.TmuxWindowOpts{
		Name:          name,
		StartDir:      startDir,
		TargetSession: sessionID,
		Command:       opts.Command,
		Background:    opts.Background,
	}, generation...)
	if err != nil {
		return "", fmt.Errorf("failed to create window: %w", err)
	}
	if opts.Background {
		return target, nil
	}
	// new-window already made it the session's active window.
	if _, err := c.focusSession(sessionID, opts.Switch, generation...); err != nil {
		return "", err
	}
	return target, nil
}

// reuseWindow drops a command into a window that already exists. The window
// already has a running process, so the command goes in as keystrokes rather
// than becoming the pane's process the way it does on create.
func (c *RealConnector) reuseWindow(target string, opts model.WindowConnectOpts, serverGeneration ...string) (string, error) {
	if opts.Command != "" {
		if _, err := c.tmux.SendKeys(target, opts.Command, serverGeneration...); err != nil {
			return "", fmt.Errorf("failed to send command to '%s': %w", target, err)
		}
	} else if opts.Background {
		// Even a no-op reuse must reject a vanished or moved selection. This
		// guarded read verifies the tuple without changing window focus.
		if _, err := c.tmux.CapturePane(target, serverGeneration...); err != nil {
			return "", fmt.Errorf("failed to verify window '%s': %w", target, err)
		}
	}
	if opts.Background {
		return target, nil
	}
	if _, err := c.tmux.SelectWindow(target, serverGeneration...); err != nil {
		return "", fmt.Errorf("failed to select window '%s': %w", target, err)
	}
	if _, err := c.focusSession(target, opts.Switch, serverGeneration...); err != nil {
		return "", err
	}
	return target, nil
}

// windowDir reads a window argument as a directory, reporting the window name
// and start directory it implies. Anything that isn't a directory is left to be
// used as a plain window name.
func (c *RealConnector) windowDir(name string) (string, string, bool) {
	if name == "" {
		return "", "", false
	}
	expanded, err := c.home.ExpandPath(name)
	if err != nil {
		return "", "", false
	}
	isDir, absPath := c.dir.Dir(expanded)
	if !isDir {
		return "", "", false
	}
	return filepath.Base(absPath), absPath, true
}
