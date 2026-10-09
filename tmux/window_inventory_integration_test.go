//go:build unix

package tmux_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

func TestTicket49LiveCwdInventoriesOnPrivateTmux(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed for the changing-cwd pane")
	}
	s := newInventoryPrivateShell(t)
	pathA := filepath.Join(s.dir, "a :: \"\\ café雪 $0 @0 %0\nfirst")
	pathB := filepath.Join(s.dir, "b ::\tlonger complete cwd $3 @3 %3\nW1::$0::\"fake\"::@0::0::1::\"fake\"::\"/fake\"::\"101:1700000000:/fake\"")
	for _, path := range []string{pathA, pathB} {
		require.NoError(t, os.MkdirAll(path, 0700))
	}
	const cycle = `import os,sys,time
a,b,body,deadline=sys.argv[1:5]
if time.monotonic()>float(deadline):sys.exit(0)
os.chdir(a);time.sleep(0.004)
os.execv('/bin/sh',['sh','-c','cd "$1"; /bin/sleep 0.004; exec "$5" -c "$2" "$3" "$1" "$2" "$4"','sh',b,body,a,deadline,sys.executable])
`
	const start = `import os,sys,time
a,b,body=sys.argv[1:4]
os.chdir(a);print('LIVE_READY',flush=True)
sys.stdin.readline()
os.execv(sys.executable,[sys.executable,'-c',body,a,b,body,str(time.monotonic()+120)])
`
	require.Equal(t, "$1 @1 %1", s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id} #{window_id} #{pane_id}", "-s", "live雪", "-n", "live", "-c", s.dir, "--", python, "-c", start, pathA, pathB, cycle))
	require.Eventually(t, func() bool {
		return s.must("-u", "display-message", "-p", "-t", "%1", "#{pane_current_path}") == pathA &&
			strings.Contains(s.must("capture-pane", "-p", "-t", "%1"), "LIVE_READY")
	}, 5*time.Second, 10*time.Millisecond)
	s.must("-u", "rename-window", "-t", "$1:@1", `live::@0 $0 %0 "\ café雪`)
	name := s.must("-u", "display-message", "-p", "-t", "$1:@1", "#{window_name}")
	generation := s.must("-u", "display-message", "-p", "#{pid}:#{start_time}:#{socket_path}")
	tm := tmux.NewTmux(nil, s, s.binary)
	s.must("send-keys", "-t", "%1", "Enter")
	var wg sync.WaitGroup
	var mu sync.Mutex
	pathsSeen := map[string]map[string]int{"ListWindows": {}, "ListAllWindows": {}}
	errorsFound := make(chan error, 4)
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				kind := "ListWindows"
				var window model.TmuxWindow
				if i%2 == 0 {
					windows, err := tm.ListWindows("$1", generation)
					if err != nil || len(windows) != 1 {
						errorsFound <- fmt.Errorf("ListWindows count=%d: %v", len(windows), err)
						return
					}
					window = *windows[0]
				} else {
					kind = "ListAllWindows"
					windows, err := tm.ListAllWindows()
					if err != nil || len(windows) != 2 || len(windows["live雪"]) != 1 || len(windows["unrelated"]) != 1 {
						errorsFound <- fmt.Errorf("ListAllWindows batch=%+v: %v", windows, err)
						return
					}
					unrelated := windows["unrelated"][0]
					if unrelated.ID != "@0" || unrelated.Name != "ordinary" || unrelated.Path != s.dir || unrelated.ServerGeneration != generation {
						errorsFound <- fmt.Errorf("changed unrelated window: %+v", unrelated)
						return
					}
					window = windows["live雪"][0]
				}
				if window.ID != "@1" || window.Index != 0 || !window.Active || window.Name != name || window.ServerGeneration != generation || (window.Path != pathA && window.Path != pathB) {
					errorsFound <- fmt.Errorf("%s mixed fields or changed identity: %+v", kind, window)
					return
				}
				mu.Lock()
				pathsSeen[kind][window.Path]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}
	for kind, paths := range pathsSeen {
		require.Equal(t, 1000, paths[pathA]+paths[pathB], kind)
		require.Positive(t, paths[pathA], kind+" must observe the first native cwd")
		require.Positive(t, paths[pathB], kind+" must observe the second native cwd")
		t.Logf("%s: 1000 concurrent collections, cwd A=%d B=%d; no mixed fields, extra/missing records or identity changes", kind, paths[pathA], paths[pathB])
	}
}

func TestTicket49SpecialCharactersAndCLocaleOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShellSocket(t, "socket '"+`"\`+"\t雪::%0")
	tm := tmux.NewTmux(nil, s, s.binary)
	generation := s.must("-u", "display-message", "-p", "#{pid}:#{start_time}:#{socket_path}")
	values := []string{"", " ", ":", "::", `"'`, `\`, "\t", "café雪", "$0 @0 %0", `W1::$0::"fake"::@0::0::1`}
	for i, text := range values {
		path := filepath.Join(s.dir, fmt.Sprintf("%d %s\nW1::$0::\"fake\"::@0::0::1\t雪", i, text))
		require.NoError(t, os.Mkdir(path, 0700))
		// Native names reject tabs; paths and synthetic fields preserve them.
		legalName := strings.ReplaceAll(text, "\t", " ")
		s.must("-u", "rename-session", "-t", "$0:", "session "+legalName)
		s.must("-u", "rename-window", "-t", "$0:@0", legalName)
		s.must("respawn-pane", "-k", "-t", "%0", "-c", path, "printf 'SPECIAL_READY\\n'; exec /bin/cat")
		s.ready("%0", "SPECIAL_READY")
		sessionName := s.must("-u", "display-message", "-p", "-t", "$0:", "#{session_name}")
		windowName := s.must("-u", "display-message", "-p", "-t", "$0:@0", "#{window_name}")
		want := model.TmuxWindow{ID: "@0", Index: 0, Active: true, Name: windowName, Path: path, ServerGeneration: generation}
		windows, err := tm.ListWindows("$0", generation)
		require.NoError(t, err)
		require.Equal(t, []*model.TmuxWindow{&want}, windows)
		all, err := tm.ListAllWindows()
		require.NoError(t, err)
		require.Equal(t, map[string][]model.TmuxWindow{sessionName: {want}}, all)
	}
	// Removing -u must lose UTF-8 bytes, while quoted boundaries still retain IDs.
	s.must("-u", "rename-session", "-t", "$0:", "café雪")
	s.must("-u", "rename-window", "-t", "$0:@0", "café雪")
	s.withoutUTF8 = true
	bad, err := tm.ListWindows("$0")
	require.NoError(t, err)
	require.Len(t, bad, 1)
	require.Equal(t, "@0", bad[0].ID)
	require.NotEqual(t, "café雪", bad[0].Name)
	all, err := tm.ListAllWindows()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.NotContains(t, all, "café雪")
	for _, windows := range all {
		require.Len(t, windows, 1)
		require.Equal(t, "@0", windows[0].ID)
	}
	s.withoutUTF8 = false
	t.Log("10 native special-character cases and complex socket bytes round-trip under C locale with TMUX absent; removing -u loses UTF-8, not identities")
}

func TestTicket49EmptyInventoriesOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShell(t)
	generation := s.must("-u", "display-message", "-p", "#{pid}:#{start_time}:#{socket_path}")
	s.must("set-option", "-g", "exit-empty", "off")
	s.must("kill-session", "-t", "$0:")
	tm := tmux.NewTmux(nil, s, s.binary)
	for _, state := range []string{"running empty server", "missing server"} {
		t.Run(state, func(t *testing.T) {
			all, err := tm.ListAllWindows()
			require.NoError(t, err)
			require.Empty(t, all)
			verified, err := tm.ListWindows("$0", generation)
			require.Error(t, err, "a vanished verified session cannot become an empty success")
			require.Nil(t, verified)
			windows, err := tm.ListWindows("")
			require.NoError(t, err)
			require.Empty(t, windows)
		})
		if state == "running empty server" {
			s.must("kill-server")
		}
	}
}
