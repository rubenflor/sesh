package connector

import (
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

// sshAttachCommand builds the initial pane command for a canonical SSH identity.
func sshAttachCommand(identity string) (string, error) {
	host, session, err := model.ParseSSHIdentity(identity)
	if err != nil {
		return "", err
	}
	target := "=" + session
	// tmux consumes an extra backslash before a trailing semicolon instead of
	// treating it as a command separator, independently of shell quoting.
	if strings.HasSuffix(target, ";") {
		target = strings.TrimSuffix(target, ";") + `\;`
	}
	remote := "tmux attach-session -t " + shell.Quote(target)
	return "exec ssh -tt -- " + shell.Quote(host) + " " + shell.Quote(remote), nil
}
