package lister

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
)

type controlledSSHExec struct {
	started        chan string
	completed      chan string
	stopped        chan probeStop
	deadlines      chan time.Duration
	release        map[string]chan struct{}
	responses      map[string]execResponse
	blockOnContext bool

	mu         sync.Mutex
	active     int
	peakActive int
}

type execResponse struct {
	result execwrap.CommandResult
	err    error
}

type probeStop struct {
	host string
	err  error
}

func newControlledSSHExec(hosts []string) *controlledSSHExec {
	return &controlledSSHExec{
		started:   make(chan string, len(hosts)),
		completed: make(chan string, len(hosts)),
		stopped:   make(chan probeStop, len(hosts)),
		deadlines: make(chan time.Duration, len(hosts)),
		release:   make(map[string]chan struct{}, len(hosts)),
		responses: make(map[string]execResponse, len(hosts)),
	}
}

func (e *controlledSSHExec) LookPath(string) (string, error) { panic("unexpected LookPath") }

func (e *controlledSSHExec) Command(string, ...string) execwrap.ExecCmd {
	panic("unexpected Command")
}

func (e *controlledSSHExec) RunContext(ctx context.Context, _ string, args ...string) (execwrap.CommandResult, error) {
	host := args[len(args)-2]
	e.mu.Lock()
	e.active++
	if e.active > e.peakActive {
		e.peakActive = e.active
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.active--
		e.mu.Unlock()
	}()

	e.started <- host
	if deadline, ok := ctx.Deadline(); ok {
		e.deadlines <- deadline.Sub(time.Now())
	}

	if e.blockOnContext {
		<-ctx.Done()
		stopErr := ctx.Err()
		e.stopped <- probeStop{host: host, err: stopErr}
		e.completed <- host
		return execwrap.CommandResult{ExitCode: -1}, stopErr
	}
	if gate := e.release[host]; gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			stopErr := ctx.Err()
			e.stopped <- probeStop{host: host, err: stopErr}
			e.completed <- host
			return execwrap.CommandResult{ExitCode: -1}, stopErr
		}
	}
	response := e.responses[host]
	e.completed <- host
	return response.result, response.err
}

func TestDiscoverSSHHostsBoundsConcurrencyAndPreservesConfiguredOrder(t *testing.T) {
	hosts := make([]string, 12)
	runner := newControlledSSHExec(hosts)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("host-%02d", i)
		runner.release[hosts[i]] = make(chan struct{})
		runner.responses[hosts[i]] = execResponse{
			result: execwrap.CommandResult{Stdout: []byte("main\n"), ExitCode: 0},
		}
	}

	type discovery struct {
		results []sshHostResult
		err     error
	}
	done := make(chan discovery, 1)
	go func() {
		results, err := discoverSSHHosts(context.Background(), runner, hosts)
		done <- discovery{results: results, err: err}
	}()

	for i := 0; i < maxConcurrentSSHProbes; i++ {
		waitSSHEvent(t, runner.started)
	}
	runner.mu.Lock()
	peak := runner.peakActive
	active := runner.active
	runner.mu.Unlock()
	if peak != maxConcurrentSSHProbes || active != maxConcurrentSSHProbes {
		t.Fatalf("initial active probes = %d, peak = %d, want %d", active, peak, maxConcurrentSSHProbes)
	}

	for _, gate := range runner.release {
		close(gate)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("discoverSSHHosts() error = %v", result.err)
	}
	if len(result.results) != len(hosts) {
		t.Fatalf("results = %d, want %d", len(result.results), len(hosts))
	}
	for i, host := range hosts {
		if result.results[i].host != host {
			t.Errorf("results[%d].host = %q, want %q", i, result.results[i].host, host)
		}
	}
	runner.mu.Lock()
	peak = runner.peakActive
	runner.mu.Unlock()
	if peak > maxConcurrentSSHProbes {
		t.Errorf("peak active probes = %d, want at most %d", peak, maxConcurrentSSHProbes)
	}
}

func TestDiscoverSSHHostsKeepsConfiguredOrderWhenProbesCompleteOutOfOrder(t *testing.T) {
	hosts := []string{"slow-a", "slow-b", "fast-c"}
	runner := newControlledSSHExec(hosts)
	for _, host := range hosts {
		runner.release[host] = make(chan struct{})
		runner.responses[host] = execResponse{
			result: execwrap.CommandResult{Stdout: []byte("main\n"), ExitCode: 0},
		}
	}

	type discovery struct {
		results []sshHostResult
		err     error
	}
	done := make(chan discovery, 1)
	go func() {
		results, err := discoverSSHHosts(context.Background(), runner, hosts)
		done <- discovery{results: results, err: err}
	}()
	for range hosts {
		waitSSHEvent(t, runner.started)
	}

	for _, host := range []string{"fast-c", "slow-b", "slow-a"} {
		close(runner.release[host])
		if got := waitSSHEvent(t, runner.completed); got != host {
			t.Fatalf("completed host = %q, want %q", got, host)
		}
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("discoverSSHHosts() error = %v", result.err)
	}
	for i, host := range hosts {
		if result.results[i].host != host {
			t.Errorf("results[%d].host = %q, want configured host %q", i, result.results[i].host, host)
		}
	}
}

func TestDiscoverSSHHostsParentCancellationStopsActiveProbes(t *testing.T) {
	hosts := []string{"one", "two", "three"}
	runner := newControlledSSHExec(hosts)
	runner.blockOnContext = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type discovery struct {
		results []sshHostResult
		err     error
	}
	done := make(chan discovery, 1)
	go func() {
		results, err := discoverSSHHosts(ctx, runner, hosts)
		done <- discovery{results: results, err: err}
	}()
	for range hosts {
		waitSSHEvent(t, runner.started)
	}
	runner.mu.Lock()
	active := runner.active
	runner.mu.Unlock()
	if active != len(hosts) {
		t.Fatalf("active probes before cancellation = %d, want %d", active, len(hosts))
	}
	cancel()
	stoppedHosts := make(map[string]bool, len(hosts))
	for range hosts {
		stopped := waitSSHEvent(t, runner.stopped)
		if !errors.Is(stopped.err, context.Canceled) {
			t.Errorf("probe %q stopped with %v, want context.Canceled (not its deadline)", stopped.host, stopped.err)
		}
		stoppedHosts[stopped.host] = true
	}
	for _, host := range hosts {
		if !stoppedHosts[host] {
			t.Errorf("probe %q did not report stopping", host)
		}
	}
	result := waitSSHEvent(t, done)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("discoverSSHHosts() error = %v, want context.Canceled", result.err)
	}
	for range hosts {
		waitSSHEvent(t, runner.completed)
	}
}

func TestDiscoverSSHHostsAppliesFiveSecondPerHostDeadline(t *testing.T) {
	hosts := []string{"slow"}
	runner := newControlledSSHExec(hosts)
	runner.blockOnContext = true

	type discovery struct {
		results []sshHostResult
		err     error
	}
	done := make(chan discovery, 1)
	go func() {
		results, err := discoverSSHHosts(context.Background(), runner, hosts)
		done <- discovery{results: results, err: err}
	}()
	waitSSHEvent(t, runner.started)
	remaining := waitSSHEvent(t, runner.deadlines)
	if remaining < sshProbeTimeout-time.Second || remaining > sshProbeTimeout {
		t.Fatalf("probe deadline is %v from start, want %v", remaining, sshProbeTimeout)
	}

	result := <-done
	if result.err != nil {
		t.Fatalf("discoverSSHHosts() parent error = %v, want per-host timeout result", result.err)
	}
	if !errors.Is(result.results[0].err, context.DeadlineExceeded) {
		t.Fatalf("host error = %v, want context.DeadlineExceeded", result.results[0].err)
	}
}

func TestAggregateSSHHostResultsFailureSemantics(t *testing.T) {
	success := sshHostResult{
		host: "reachable",
		sessions: model.SeshSessions{
			OrderedIndex: []string{"ssh:ssh/reachable/main"},
			Directory: model.SeshSessionMap{
				"ssh:ssh/reachable/main": {Src: "ssh", Name: "ssh/reachable/main"},
			},
		},
	}
	emptySuccess := sshHostResult{host: "empty", sessions: model.SeshSessions{}}
	failure := sshHostResult{host: "offline", err: errors.New("SSH discovery for host offline failed: connection refused")}

	tests := []struct {
		name             string
		results          []sshHostResult
		failWhenAllHosts bool
		wantSessions     []string
		wantError        string
	}{
		{
			name:         "mixed listing retains successful hosts and suppresses failures",
			results:      []sshHostResult{failure, success},
			wantSessions: []string{"ssh:ssh/reachable/main"},
		},
		{
			name:         "mixed listing suppresses all SSH host failures",
			results:      []sshHostResult{failure},
			wantSessions: []string{},
		},
		{
			name:             "SSH-only listing allows partial failures",
			results:          []sshHostResult{failure, success},
			failWhenAllHosts: true,
			wantSessions:     []string{"ssh:ssh/reachable/main"},
		},
		{
			name:             "successful empty host is not an outage",
			results:          []sshHostResult{emptySuccess},
			failWhenAllHosts: true,
		},
		{
			name:             "SSH-only all-host failure returns host context",
			results:          []sshHostResult{failure, {host: "timeout", err: errors.New("SSH discovery for host timeout: context deadline exceeded")}},
			failWhenAllHosts: true,
			wantError:        "offline",
		},
		{
			name:             "no configured hosts is successful empty inventory",
			failWhenAllHosts: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := aggregateSSHHostResults(tt.results, tt.failWhenAllHosts)
			if tt.wantError == "" && err != nil {
				t.Fatalf("aggregateSSHHostResults() error = %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("aggregateSSHHostResults() error = %v, want host context %q", err, tt.wantError)
			}
			if fmt.Sprint(got.OrderedIndex) != fmt.Sprint(tt.wantSessions) {
				t.Errorf("OrderedIndex = %v, want %v", got.OrderedIndex, tt.wantSessions)
			}
		})
	}
}

func waitSSHEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(10 * time.Second):
		var zero T
		t.Fatal("timed out waiting for SSH probe event")
		return zero
	}
}
