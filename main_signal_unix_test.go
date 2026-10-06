//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCLISignalsCancelSSHDiscovery(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("PTY fixture supports macOS and Linux")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script is required for the real picker terminal")
	}
	bin := filepath.Join(t.TempDir(), "sesh")
	build := exec.Command("go", "build", "-o", bin, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, output)
	}
	for _, command := range []string{"list", "picker"} {
		signals := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM}
		if command == "list" {
			signals = append(signals, 0)
		} // Real five-second probe deadline.
		for _, sig := range signals {
			label := sig.String()
			if sig == 0 {
				label = "deadline"
			}
			t.Run(fmt.Sprintf("%s/%s", command, label), func(t *testing.T) {
				dir := t.TempDir()
				write := func(name, text string) string {
					p := filepath.Join(dir, name)
					if err := os.WriteFile(p, []byte(text), 0700); err != nil {
						t.Fatal(err)
					}
					return p
				}
				config := write("config.toml", "[ssh]\nhosts = ['fake']\n")
				write("ssh", "#!/bin/sh\nexec \"$SESH_SIGNAL_HELPER\" -test.run=^TestSSHSignalHelper$ -- parent\n")
				wrapper := write("cli", "#!/bin/sh\necho $$ > \"$SESH_SIGNAL_DIR/cli\"\nexec \"$SESH_SIGNAL_CLI\" --config \"$SESH_SIGNAL_CONFIG\" \"$SESH_SIGNAL_COMMAND\" --ssh\n")
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				var process *exec.Cmd
				if command == "picker" {
					if runtime.GOOS == "darwin" {
						process = exec.CommandContext(ctx, script, "-q", "/dev/null", wrapper)
					} else {
						process = exec.CommandContext(ctx, script, "-q", "-e", "-c", "'"+strings.ReplaceAll(wrapper, "'", "'\\''")+"'", "/dev/null")
					}
				} else {
					process = exec.CommandContext(ctx, wrapper)
				}
				process.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "SESH_SIGNAL_HELPER="+os.Args[0], "SESH_SIGNAL_DIR="+dir, "SESH_SIGNAL_CLI="+bin, "SESH_SIGNAL_CONFIG="+config, "SESH_SIGNAL_COMMAND="+command, "TERM=xterm-256color", "ENV=", "XDG_CACHE_HOME="+dir)
				var output bytes.Buffer
				input, err := process.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = input.Close() })
				terminal := &signalTerminal{output: &output, input: input}
				process.Stdout, process.Stderr = terminal, terminal
				if err := process.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- process.Wait() }()
				t.Cleanup(func() {
					for _, name := range []string{"cli", "parent", "descendant"} {
						if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
							pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
							if pid > 0 {
								_ = syscall.Kill(pid, syscall.SIGKILL)
							}
						}
					}
					_ = process.Process.Kill()
				})
				pid := waitSignalPID(t, dir, "parent", done)
				cli := waitSignalPID(t, dir, "cli", done)
				allowance := 3 * time.Second
				message := "context canceled"
				if sig == 0 {
					allowance = 8 * time.Second
					message = "all configured ssh hosts failed"
				} else {
					if err := syscall.Kill(cli, sig); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case err := <-done:
					if command == "list" {
						var exitErr *exec.ExitError
						if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
							t.Fatalf("CLI cancellation/timeout status = %v; want 1", err)
						}
					}
					if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
						t.Fatalf("CLI exited without terminating/reaping SSH PID %d: %v; output %s", pid, err, &output)
					}
					if !strings.Contains(strings.ToLower(output.String()), message) {
						t.Fatalf("CLI did not report %q: %s", message, &output)
					}
				case <-time.After(allowance):
					t.Fatal("CLI did not complete within bounded cancellation allowance")
				}
			})
		}
	}
}

// script provides a PTY, not a terminal emulator. Answer Fang's background-color
// query so error rendering cannot wait for a nonexistent terminal response.
type signalTerminal struct {
	output  *bytes.Buffer
	input   io.Writer
	pending string
}

func (s *signalTerminal) Write(p []byte) (int, error) {
	n, err := s.output.Write(p)
	s.pending += string(p)
	const query = "\x1b]11;?"
	for {
		index := strings.Index(s.pending, query)
		if index < 0 {
			break
		}
		_, _ = io.WriteString(s.input, "\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[?1;2c")
		s.pending = s.pending[index+len(query):]
	}
	if len(s.pending) > len(query) {
		s.pending = s.pending[len(s.pending)-len(query):]
	}
	return n, err
}

func waitSignalPID(t *testing.T, dir, name string, done <-chan error) int {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		select {
		case err := <-done:
			t.Fatalf("CLI exited before %s readiness: %v", name, err)
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s readiness", name)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSSHSignalHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	dir, mode := os.Getenv("SESH_SIGNAL_DIR"), os.Args[len(os.Args)-1]
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestSSHSignalHelper$", "--", "descendant")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "descendant")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, mode), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		panic(err)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
