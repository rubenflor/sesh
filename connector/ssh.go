package connector

import (
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
)

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
