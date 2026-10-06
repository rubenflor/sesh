package lister

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
)

// Starting a configuration-free server makes an absent server a successful empty
// inventory without creating sessions. Existing servers retain their settings.
// Never normalize failure statuses: socket errors also return status 1. The
// header proves the command reached the server even if startup exits with 0.
const sshInventoryHeader = "sesh-ssh-inventory-v1\n"
const sshDiscoveryCommand = `tmux -u -f /dev/null start-server \; display-message -p sesh-ssh-inventory-v1 \; list-sessions -F '#{session_name}'`

const (
	maxConcurrentSSHProbes = 8
	sshProbeTimeout        = 5 * time.Second
)

type sshHostResult struct {
	host     string
	sessions model.SeshSessions
	err      error
}

func listSSH(l *RealLister, ctx context.Context, failWhenAllHostsFail bool) (model.SeshSessions, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	results, err := discoverSSHHosts(ctx, l.exec, l.config.SSH.Hosts)
	if err != nil {
		return model.SeshSessions{}, err
	}
	return aggregateSSHHostResults(results, failWhenAllHostsFail)
}

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

func discoverSSHHosts(ctx context.Context, runner execwrap.Exec, hosts []string) ([]sshHostResult, error) {
	results := make([]sshHostResult, len(hosts))
	if len(hosts) == 0 {
		return results, nil
	}

	workerCount := min(len(hosts), maxConcurrentSSHProbes)
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				host := hosts[index]
				if err := ctx.Err(); err != nil {
					results[index] = sshHostResult{host: host, err: err}
					continue
				}

				hostCtx, cancel := context.WithTimeout(ctx, sshProbeTimeout)
				sessions, err := probeSSHHost(hostCtx, runner, host)
				cancel()
				results[index] = sshHostResult{host: host, sessions: sessions, err: err}
			}
		}()
	}

dispatch:
	for index := range hosts {
		select {
		case <-ctx.Done():
			for pending := index; pending < len(hosts); pending++ {
				results[pending] = sshHostResult{host: hosts[pending], err: ctx.Err()}
			}
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return results, err
	}
	return results, nil
}

func aggregateSSHHostResults(results []sshHostResult, failWhenAllHostsFail bool) (model.SeshSessions, error) {
	sessions := model.SeshSessions{
		Directory:    make(model.SeshSessionMap),
		OrderedIndex: make([]string, 0),
	}
	failedHosts := make([]error, 0, len(results))
	successfulHosts := 0

	for _, result := range results {
		if result.err != nil {
			failedHosts = append(failedHosts, result.err)
			continue
		}
		successfulHosts++
		for _, key := range result.sessions.OrderedIndex {
			sessions.OrderedIndex = append(sessions.OrderedIndex, key)
			sessions.Directory[key] = result.sessions.Directory[key]
		}
	}

	if failWhenAllHostsFail && len(results) > 0 && successfulHosts == 0 {
		return model.SeshSessions{}, fmt.Errorf("all configured SSH hosts failed: %w", errors.Join(failedHosts...))
	}
	return sessions, nil
}

func parseTmuxSessionNames(output []byte) ([]string, error) {
	inventory, ok := strings.CutPrefix(string(output), sshInventoryHeader)
	if !ok || (inventory != "" && !strings.HasSuffix(inventory, "\n")) {
		return nil, fmt.Errorf("missing inventory header or incomplete record")
	}
	if inventory == "" {
		return nil, nil
	}

	lines := strings.Split(strings.TrimSuffix(inventory, "\n"), "\n")
	for i, line := range lines {
		name, err := decodeTmuxSessionName(line)
		if err != nil {
			return nil, err
		}
		if name == "" || strings.ContainsRune(name, '\x00') {
			return nil, fmt.Errorf("empty or invalid session name in inventory")
		}
		lines[i] = name
	}
	return lines, nil
}

// tmux sanitizes session names with vis(VIS_OCTAL|VIS_CSTYLE|VIS_TAB|VIS_NL).
// Decode once, so a literal backslash-n remains distinct from a newline.
func decodeTmuxSessionName(line string) (string, error) {
	var name strings.Builder
	for i := 0; i < len(line); i++ {
		b := line[i]
		if b != '\\' {
			if b < 0x20 || b == 0x7f {
				return "", fmt.Errorf("unescaped control character in inventory")
			}
			name.WriteByte(b)
			continue
		}
		i++
		if i == len(line) {
			return "", fmt.Errorf("incomplete tmux escape in inventory")
		}
		b = line[i]
		const escapes = "abtnvfrs\\"
		const decoded = "\a\b\t\n\v\f\r \\"
		if index := strings.IndexByte(escapes, b); index >= 0 {
			name.WriteByte(decoded[index])
		} else if b >= '0' && b <= '7' && i+2 < len(line) {
			value, err := strconv.ParseUint(line[i:i+3], 8, 8)
			if err != nil {
				return "", fmt.Errorf("invalid octal tmux escape in inventory: %w", err)
			}
			name.WriteByte(byte(value))
			i += 2
		} else {
			return "", fmt.Errorf("invalid tmux escape in inventory")
		}
	}
	return name.String(), nil
}
