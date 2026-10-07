//go:build unix

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	bridgeNeverStarted = "never started"
	bridgeUnresolved   = "identity unresolved"
	bridgeIdentified   = "identified"
	bridgeStopped      = "confirmed stopped"
	bridgeServerFormat = "#{pid}:#{start_time}:#{socket_path}"
)

type bridgeServerStart struct {
	Socket       string `json:"socket"`
	State        string `json:"state"`
	Generation   string `json:"generation,omitempty"`
	PID          int    `json:"pid,omitempty"`
	StartupError string `json:"startup_error,omitempty"`
	CleanupError string `json:"cleanup_error,omitempty"`
}

// Only the ticket-33 fixture uses this accounting. Commands remain bounded by
// panePrivateShell.run and always address its exact private socket.
type bridgePrivateFixture struct {
	dir, socket string
	run         func(...string) (string, error)
	starts      []*bridgeServerStart
	removed     bool
}

func newBridgePrivateFixture(t *testing.T, binary string) (*bridgePrivateFixture, *panePrivateShell) {
	t.Helper()
	tempRoot := filepath.Clean(os.TempDir())
	if !strings.HasPrefix(tempRoot, "/tmp/") && !strings.HasPrefix(tempRoot, "/private/tmp/") {
		tempRoot = "/tmp"
	}
	dir, err := os.MkdirTemp(tempRoot, "sesh-bridge-")
	require.NoError(t, err)
	sh := &panePrivateShell{binary: binary, socket: filepath.Join(dir, "socket")}
	f := &bridgePrivateFixture{dir: dir, socket: sh.socket, run: sh.run}
	t.Cleanup(func() {
		// Restore the native runner even if an injected failure caused an assertion
		// to abort a cleanup regression before it could restore the runner itself.
		f.run = sh.run
		if err := f.cleanup(); err != nil {
			t.Error(err)
		}
	})
	for _, variable := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		path := filepath.Join(dir, strings.ToLower(variable))
		require.NoError(t, os.MkdirAll(path, 0700))
		t.Setenv(variable, path)
	}
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")
	t.Setenv("LC_ALL", "C")
	return f, sh
}

func (f *bridgePrivateFixture) save() error {
	data, err := json.MarshalIndent(f.starts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.dir, "servers.json"), append(data, '\n'), 0600)
}

func bridgeServerPID(generation, socket string) (int, error) {
	parts := strings.SplitN(generation, ":", 3)
	if len(parts) == 3 {
		pid, pidErr := strconv.Atoi(parts[0])
		started, startErr := strconv.ParseUint(parts[1], 10, 64)
		resolved, pathErr := filepath.EvalSymlinks(socket)
		if pidErr == nil && pid > 0 && startErr == nil && started > 0 && pathErr == nil && parts[2] == resolved {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("invalid server identity %q for private socket %q", generation, socket)
}

func (f *bridgePrivateFixture) startAnchor() error {
	record := &bridgeServerStart{Socket: f.socket, State: bridgeUnresolved}
	f.starts = append(f.starts, record)
	// Persist before the command can spawn, including before any assertion can
	// abort the test. A failed tmux command may still have created its server.
	if err := f.save(); err != nil {
		return err
	}
	out, err := f.run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "anchor", "--", "exec sleep 120")
	if err != nil {
		var pathErr *os.PathError
		var execErr *exec.Error
		if errors.As(err, &execErr) || (errors.As(err, &pathErr) && pathErr.Op == "fork/exec") {
			record.State = bridgeNeverStarted // The tmux executable itself never started.
		}
	} else if out != "$0" {
		err = fmt.Errorf("private anchor returned unexpected session ID %q", out)
	} else {
		record.Generation, err = f.run("display-message", "-p", "-t", "$0:", bridgeServerFormat)
		if err == nil {
			record.PID, err = bridgeServerPID(record.Generation, record.Socket)
			if err == nil {
				record.State = bridgeIdentified
			}
		}
	}
	if err != nil {
		record.StartupError = err.Error()
	}
	return errors.Join(err, f.save())
}

func bridgeSocketAbsent(socket string, args []string, err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return false
	}
	// panePrivateShell.run preserves native stderr in this exact error envelope.
	// Accept only tmux's explicit missing-server diagnostics for this socket;
	// timeouts, transport failures and other command errors remain ambiguous.
	prefix := fmt.Sprintf("private tmux %v: %s: ", args, exitErr.Error())
	if !strings.HasPrefix(err.Error(), prefix) {
		return false
	}
	message := strings.TrimSuffix(strings.TrimPrefix(err.Error(), prefix), "\n")
	return message == "error connecting to "+socket+" (No such file or directory)" ||
		message == "no server running on "+socket
}

func (f *bridgePrivateFixture) stop() error {
	var pending []*bridgeServerStart
	for _, record := range f.starts {
		if record.State != bridgeStopped && record.State != bridgeNeverStarted {
			pending = append(pending, record)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	_, killErr := f.run("kill-server")
	args := []string{"display-message", "-p", bridgeServerFormat}
	deadline := time.Now().Add(3 * time.Second)
	var verifyErr error
	for {
		out, err := f.run(args...)
		if out == "" && bridgeSocketAbsent(f.socket, args, err) {
			break
		}
		if err != nil {
			verifyErr = fmt.Errorf("shutdown query for %q was ambiguous: %w", f.socket, err)
			break
		}
		if _, err := bridgeServerPID(out, f.socket); err != nil {
			verifyErr = err
			break
		}
		if time.Now().After(deadline) {
			verifyErr = fmt.Errorf("private server still responds on %q as %q", f.socket, out)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if verifyErr == nil {
		for _, record := range pending {
			// PID probes are read-only and never authorize termination. The full
			// identity was captured through our socket; PID reuse conservatively
			// retains the fixture rather than authorizing a kill or assuming exit.
			if record.PID != 0 {
				for syscall.Kill(record.PID, 0) == nil && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if err := syscall.Kill(record.PID, 0); err != syscall.ESRCH {
					verifyErr = errors.Join(verifyErr, fmt.Errorf("exit unverified for %q (%s), PID probe: %v", record.Socket, record.Generation, err))
					continue
				}
			}
			record.State = bridgeStopped
			record.CleanupError = ""
		}
	}
	if verifyErr != nil {
		verifyErr = errors.Join(killErr, verifyErr)
		for _, record := range pending {
			if record.State != bridgeStopped {
				record.CleanupError = verifyErr.Error()
			}
		}
	}
	return errors.Join(verifyErr, f.save())
}

func (f *bridgePrivateFixture) cleanup() error {
	if f.removed {
		return nil
	}
	if err := f.stop(); err != nil {
		return fmt.Errorf("private tmux cleanup incomplete; retaining %s (socket %s, records %s): %w",
			f.dir, f.socket, filepath.Join(f.dir, "servers.json"), err)
	}
	if err := os.RemoveAll(f.dir); err != nil {
		return fmt.Errorf("remove stopped private tmux fixture %s: %w", f.dir, err)
	}
	f.removed = true
	return nil
}

func TestSSHBridgeFixtureCleanupOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, mode := range []string{"identified shutdown", "identity capture fails", "shutdown query fails"} {
		t.Run(mode, func(t *testing.T) {
			f, sh := newBridgePrivateFixture(t, binary)
			captureErr := errors.New("injected identity capture failure")
			if mode == "identity capture fails" {
				f.run = func(args ...string) (string, error) {
					if args[0] == "display-message" {
						return "", captureErr
					}
					return sh.run(args...)
				}
				require.ErrorIs(t, f.startAnchor(), captureErr)
				require.Equal(t, bridgeUnresolved, f.starts[0].State)
				require.Zero(t, f.starts[0].PID)
			} else {
				require.NoError(t, f.startAnchor())
				require.Equal(t, bridgeIdentified, f.starts[0].State)
				require.NotEmpty(t, f.starts[0].Generation)
			}
			// The unresolved case really did create a server, not merely a mock.
			generation, err := sh.run("display-message", "-p", "-t", "$0:", bridgeServerFormat)
			require.NoError(t, err)
			_, err = bridgeServerPID(generation, sh.socket)
			require.NoError(t, err)
			if mode == "identified shutdown" {
				require.NoError(t, f.cleanup())
				require.Equal(t, bridgeStopped, f.starts[0].State)
				require.NoDirExists(t, f.dir)
				require.NoError(t, f.cleanup(), "cleanup is idempotent")
				return
			}
			killErr := errors.New("injected kill-server failure")
			queryErr := errors.New("injected transport timeout")
			f.run = func(args ...string) (string, error) {
				if args[0] == "kill-server" {
					return "", killErr
				}
				return "", queryErr
			}
			err = f.cleanup()
			require.ErrorIs(t, err, killErr)
			require.ErrorIs(t, err, queryErr)
			require.ErrorContains(t, err, "retaining "+f.dir)
			require.ErrorContains(t, err, f.socket)
			require.DirExists(t, f.dir)
			require.FileExists(t, f.socket)
			data, err := os.ReadFile(filepath.Join(f.dir, "servers.json"))
			require.NoError(t, err)
			var records []*bridgeServerStart
			require.NoError(t, json.Unmarshal(data, &records))
			require.Len(t, records, 1)
			require.Equal(t, f.starts[0], records[0], "retain the full startup and cleanup diagnostics")
			_, err = sh.run("display-message", "-p", "-t", "$0:", bridgeServerFormat)
			require.NoError(t, err, "the injected ambiguity did not secretly stop the server")
			// Registered cleanup restores the native runner and verifies real exit.
		})
	}
}

func TestSSHBridgeFixtureExitCodeHelper(t *testing.T) {
	if value := os.Getenv("SESH_BRIDGE_FIXTURE_EXIT_CODE"); value != "" {
		code, err := strconv.Atoi(value)
		if err != nil {
			os.Exit(3)
		}
		os.Exit(code)
	}
}

func bridgeFixtureExitError(t *testing.T, code int) *exec.ExitError {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestSSHBridgeFixtureExitCodeHelper$")
	cmd.Env = append(os.Environ(), "SESH_BRIDGE_FIXTURE_EXIT_CODE="+strconv.Itoa(code))
	var exitErr *exec.ExitError
	require.ErrorAs(t, cmd.Run(), &exitErr)
	require.Equal(t, code, exitErr.ExitCode())
	return exitErr
}

func TestSSHBridgeFixtureCleanupEvidence(t *testing.T) {
	exitOne, exitTwo := bridgeFixtureExitError(t, 1), bridgeFixtureExitError(t, 2)
	for _, mode := range []string{
		"socket absent", "no server", "wrong socket", "timeout", "transport error",
		"malformed response", "execution failure", "wrong exit status", "extra diagnostics", "unexpected stdout",
	} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp(t.TempDir(), "fixture-")
			require.NoError(t, err)
			f := &bridgePrivateFixture{dir: dir, socket: filepath.Join(dir, "socket")}
			// A placeholder lets us verify that ambiguous cleanup does not unlink
			// socket state; the injected runner cannot start or contact any server.
			require.NoError(t, os.WriteFile(f.socket, nil, 0600))
			captureErr := errors.New("identity query failed after startup")
			f.run = func(args ...string) (string, error) {
				if args[0] == "new-session" {
					data, err := os.ReadFile(filepath.Join(dir, "servers.json"))
					require.NoError(t, err)
					var records []*bridgeServerStart
					require.NoError(t, json.Unmarshal(data, &records))
					require.Len(t, records, 1, "account for startup before invoking tmux")
					require.Equal(t, f.socket, records[0].Socket)
					require.Equal(t, bridgeUnresolved, records[0].State)
					return "$0", nil
				}
				return "", captureErr
			}
			require.ErrorIs(t, f.startAnchor(), captureErr)
			killErr := errors.New("kill-server failed")
			f.run = func(args ...string) (string, error) {
				if args[0] == "kill-server" {
					return "", killErr
				}
				out := ""
				diagnostic := "no server running on " + f.socket
				exitErr := exitOne
				switch mode {
				case "socket absent":
					diagnostic = "error connecting to " + f.socket + " (No such file or directory)"
				case "wrong socket":
					diagnostic = "no server running on " + f.socket + "-other"
				case "timeout":
					return "", context.DeadlineExceeded
				case "transport error":
					diagnostic = "error connecting to " + f.socket + " (Connection refused)"
				case "malformed response":
					return "not a server identity", nil
				case "execution failure":
					return "", &os.PathError{Op: "fork/exec", Path: "missing-tmux", Err: syscall.ENOENT}
				case "wrong exit status":
					exitErr = exitTwo
				case "extra diagnostics":
					diagnostic = "transport failed\n" + diagnostic
				case "unexpected stdout":
					out = "unexpected output"
				}
				return out, fmt.Errorf("private tmux %v: %w: %s\n", args, exitErr, diagnostic)
			}
			err = f.cleanup()
			if mode == "socket absent" || mode == "no server" {
				require.NoError(t, err, "positive stopped-server evidence permits cleanup despite kill failure")
				require.Equal(t, bridgeStopped, f.starts[0].State)
				require.NoDirExists(t, dir)
			} else {
				require.ErrorIs(t, err, killErr)
				require.ErrorContains(t, err, "retaining "+dir)
				require.Equal(t, bridgeUnresolved, f.starts[0].State)
				require.FileExists(t, f.socket)
				data, readErr := os.ReadFile(filepath.Join(dir, "servers.json"))
				require.NoError(t, readErr)
				require.Contains(t, string(data), captureErr.Error())
				require.Contains(t, string(data), "cleanup_error")
			}
		})
	}
}

func TestSSHBridgeFixtureNeverStarted(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "fixture-")
	require.NoError(t, err)
	f := &bridgePrivateFixture{dir: dir, socket: filepath.Join(dir, "socket")}
	creationErr := &os.PathError{Op: "fork/exec", Path: "missing-tmux", Err: syscall.ENOENT}
	f.run = func(args ...string) (string, error) {
		require.Equal(t, "new-session", args[0], "no shutdown command is needed when exec never started")
		return "", creationErr
	}
	require.ErrorIs(t, f.startAnchor(), creationErr)
	require.Equal(t, bridgeNeverStarted, f.starts[0].State)
	require.NoError(t, f.cleanup())
	require.NoDirExists(t, dir)
}
