package lister

import (
	"context"
	"fmt"
	"strings"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
)

// tmux returns status 1 when it has no server or sessions. The remote shell
// turns only that status into success; other tmux failures remain failures.
const sshDiscoveryCommand = `sh -c 'tmux list-sessions -F "#{session_name}" || test "$?" -eq 1'`

func probeSSHHost(ctx context.Context, runner execwrap.Exec, host string) (model.SeshSessions, error) {
	result, err := runner.RunContext(
		ctx,
		"ssh",
		"-T",
		"-n",
		"-o",
		"BatchMode=yes",
		"--",
		host,
		sshDiscoveryCommand,
	)
	if err != nil {
		return model.SeshSessions{}, fmt.Errorf("SSH discovery for host %q failed: %w", host, err)
	}
	if result.ExitCode != 0 {
		return model.SeshSessions{}, fmt.Errorf("SSH discovery for host %q exited with status %d", host, result.ExitCode)
	}

	names, err := parseTmuxSessionNames(result.Stdout)
	if err != nil {
		return model.SeshSessions{}, fmt.Errorf("SSH discovery for host %q returned invalid tmux output: %w", host, err)
	}

	sessions := model.SeshSessions{
		Directory:    make(model.SeshSessionMap, len(names)),
		OrderedIndex: make([]string, 0, len(names)),
	}
	for _, name := range names {
		identity, err := model.EncodeSSHIdentity(host, name)
		if err != nil {
			return model.SeshSessions{}, fmt.Errorf("SSH discovery for host %q returned an invalid session name: %w", host, err)
		}
		key := "ssh:" + identity
		if _, exists := sessions.Directory[key]; exists {
			return model.SeshSessions{}, fmt.Errorf("SSH discovery for host %q returned duplicate session %q", host, name)
		}
		sessions.OrderedIndex = append(sessions.OrderedIndex, key)
		sessions.Directory[key] = model.SeshSession{
			Src:  "ssh",
			Name: identity,
		}
	}
	return sessions, nil
}

func parseTmuxSessionNames(output []byte) ([]string, error) {
	if len(output) == 0 {
		return nil, nil
	}

	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	for _, name := range lines {
		if name == "" || strings.ContainsRune(name, '\x00') {
			return nil, fmt.Errorf("empty or invalid session name in inventory")
		}
	}
	return lines, nil
}
