package connector

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
)

func sshStrategy(c *RealConnector, name string) (model.Connection, error) {
	if !strings.HasPrefix(name, "ssh/") {
		return model.Connection{Found: false}, nil
	}
	host, _, err := model.ParseSSHIdentity(name)
	if err != nil {
		return model.Connection{}, err
	}
	if !slices.Contains(c.config.SSH.Hosts, host) {
		return model.Connection{}, fmt.Errorf("SSH host %q is not configured in [ssh].hosts", host)
	}
	return model.Connection{
		Found: true, Session: model.SeshSession{Src: "ssh", Name: name},
	}, nil
}

func connectToSSH(c *RealConnector, connection model.Connection, opts model.ConnectOpts) (string, error) {
	if opts.Command != "" {
		return "", errors.New("--command is not supported for SSH sessions")
	}
	identity := connection.Session.Name
	if opts.Switch {
		clients, err := c.tmux.ListClients()
		if err != nil {
			return "", fmt.Errorf("couldn't reach tmux to switch to SSH session %q: %w", identity, err)
		}
		if len(clients) == 0 {
			return "", fmt.Errorf("cannot switch to SSH session %q: no attached local tmux client; attach a local client or run sesh connect without --switch", identity)
		}
	}
	bridge, err := c.ensureSSHBridge(identity)
	if err != nil {
		return "", err
	}
	if _, err := c.focusSession(bridge, opts.Switch); err != nil {
		return "", fmt.Errorf("failed to connect to SSH session %q: %w", identity, err)
	}
	return fmt.Sprintf("connected to SSH session: %s", identity), nil
}

// sshAttachCommand builds the initial pane command for a canonical SSH identity.
func sshAttachCommand(identity string) (string, error) {
	host, session, err := model.ParseSSHIdentity(identity)
	if err != nil {
		return "", err
	}
	target := "=" + tmuxSSHSessionTarget(session)
	// tmux consumes an extra backslash before a trailing semicolon instead of
	// treating it as a command separator, independently of shell quoting.
	if strings.HasSuffix(target, ";") {
		target = strings.TrimSuffix(target, ";") + `\;`
	}
	remote := "tmux attach-session -t " + shell.Quote(target)
	return "exec ssh -tt -- " + shell.Quote(host) + " " + shell.Quote(remote), nil
}

// Exact target lookup uses tmux's stored vis-escaped name, while the public SSH
// identity carries the original bytes. Match session_check_name's escaping.
func tmuxSSHSessionTarget(session string) string {
	var target strings.Builder
	for i := 0; i < len(session); i++ {
		b := session[i]
		const controls = "\a\b\t\n\v\f\r\\"
		const escapes = "abtnvfr\\"
		if index := strings.IndexByte(controls, b); index >= 0 {
			target.WriteByte('\\')
			target.WriteByte(escapes[index])
		} else if b < 0x20 || b == 0x7f {
			fmt.Fprintf(&target, "\\%03o", b)
		} else {
			target.WriteByte(b)
		}
	}
	return target.String()
}

func (c *RealConnector) ensureSSHBridge(identity string) (string, error) {
	command, err := sshAttachCommand(identity)
	if err != nil {
		return "", err
	}
	sessions, err := c.tmux.ListSessions()
	if err != nil {
		return "", fmt.Errorf("failed to list SSH bridges: %w", err)
	}
	occupied := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		occupied[session.Name] = true
		// Server-assigned IDs prevent name-shaped targets from redirecting lookup.
		marker, err := c.tmux.GetSessionEnvironment(session.ID, model.SSHBridgeIdentityEnv)
		if err != nil {
			return "", fmt.Errorf("failed to read SSH bridge marker for %q: %w", session.Name, err)
		}
		if marker == identity {
			dead, err := c.tmux.IsPaneDead(session.ID)
			if err != nil {
				return "", fmt.Errorf("failed to inspect SSH bridge %q: %w", session.Name, err)
			}
			if !dead {
				return session.ID, nil
			}
			if _, err := c.tmux.KillSessionInGeneration(session.ID, session.ServerGeneration); err != nil {
				return "", fmt.Errorf("failed to remove stale SSH bridge %q: %w", session.Name, err)
			}
			delete(occupied, session.Name)
		}
	}
	digest := sha256.Sum256([]byte(identity))
	for slot := 0; slot < 8; slot++ {
		name := fmt.Sprintf("sesh-ssh-%x-%d", digest[:12], slot)
		if occupied[name] {
			continue
		}
		created, err := c.tmux.NewSessionWithCommand(model.TmuxSessionOpts{
			Name: name, Command: command,
			Environment: []string{model.SSHBridgeIdentityEnv + "=" + identity},
			SessionOptions: []model.TmuxOption{
				{Name: "status", Value: "off"},
				{Name: "detach-on-destroy", Value: "off"},
			},
			PaneOptions: []model.TmuxOption{
				{Name: "remain-on-exit", Value: "failed"},
				{Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."},
			},
		})
		if err != nil {
			return "", fmt.Errorf("failed to create SSH bridge %q: %w", name, err)
		}
		ids := strings.Fields(created)
		if len(ids) != 2 || !strings.HasPrefix(ids[0], "$") || !strings.HasPrefix(ids[1], "%") {
			return "", fmt.Errorf("tmux returned unreadable SSH bridge session/pane IDs: %q", created)
		}
		return ids[0], nil
	}
	return "", fmt.Errorf("SSH bridge name collision for %q: all 8 slots are occupied", identity)
}
