//go:build unix

package picker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/shell"
)

func TestFZFDirectoryCancellationCleansProcessGroup(t *testing.T) {
	for _, mode := range []string{"Escape", "source change", "parent cancellation"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			pidsFile := filepath.Join(root, "pids")
			binary := filepath.Join(root, "fd")
			require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nsleep 60 &\nprintf '%s %s' \"$$\" \"$!\" > "+shell.Quote(pidsFile)+"\nwait\n"), 0700))
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			executor := execwrap.NewMockExec(t)
			executor.EXPECT().LookPath("fd").Return(binary, nil).Once()
			h := home.NewMockHome(t)
			h.EXPECT().ExpandPath("~").Return(root, nil).Once()
			p := &RealPicker{home: h, shell: shell.NewShell(executor, h)}
			m := fzfModel(sshPickerSessions(), testOptionsWith(func(o *Options) {
				o.Context, o.FindDirectories = ctx, p.findDirectories
			}))
			next, cmd := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
			m = next.(Model)
			done := make(chan tea.Msg, 1)
			finished := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("fd fixture cleanup timed out")
				}
			})
			go func() { done <- cmd(); close(finished) }()
			var pids []string
			require.Eventually(t, func() bool {
				data, _ := os.ReadFile(pidsFile)
				pids = strings.Fields(string(data))
				return len(pids) == 2
			}, 3*time.Second, 10*time.Millisecond)
			if mode == "parent cancellation" {
				cancel()
			} else if mode == "Escape" {
				m = press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
			} else {
				m = press(m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
			}
			select {
			case msg := <-done:
				m = press(m, msg)
			case <-time.After(3 * time.Second):
				t.Fatal("fd cancellation did not finish and reap its child")
			}
			for _, raw := range pids {
				pid, err := strconv.Atoi(raw)
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
						return true
					}
					out, _ := exec.Command("ps", "-o", "stat=", "-p", raw).Output()
					return strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
				}, 3*time.Second, 10*time.Millisecond, "owned fd process %s remains alive", raw)
			}
		})
	}
}
