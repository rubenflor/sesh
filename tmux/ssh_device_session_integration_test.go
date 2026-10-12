//go:build unix

package tmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

func TestSSHDeviceSessionRelationOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShell(t)
	t.Chdir(s.dir)
	s.must("set-option", "-g", "default-command", "exec /bin/cat")
	s.must("set-option", "-g", "destroy-unattached", "off")
	for _, names := range [][2]string{
		{"$0", "$1"},
		{"a/b%2F café雪 \\n", "-device '$HOME;$(touch MUST_NOT_RUN);#{session_id}"},
	} {
		t.Run(names[0], func(t *testing.T) {
			s.t = t
			original := s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id}", "-s", strings.ReplaceAll(names[0], "#", "##"), "-n", "duplicate", "--", "/bin/cat")
			first := s.must("display-message", "-p", "-t", original+":", "#{window_id}")
			second := s.must("new-window", "-d", "-P", "-F", "#{window_id}", "-t", original+":1", "-n", "duplicate", "--", "/bin/cat")
			s.must("link-window", "-d", "-s", original+":"+first, "-t", original+":3")
			device := s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id}", "-s", strings.ReplaceAll(names[1], "#", "##"), "-t", original)
			s.must("select-window", "-t", original+":"+first)
			s.must("select-window", "-t", device+":"+second)
			for i, id := range []string{original, device} {
				require.Equal(t, model.TmuxSSHSessionTarget(names[i]), s.must("-u", "display-message", "-p", "-t", id+":", "#{session_name}"))
			}
			require.NotEqual(t, original, device)
			originalName, err := model.EncodeSSHIdentity("dev", names[0])
			require.NoError(t, err)
			deviceName, err := model.EncodeSSHIdentity("dev", names[1])
			require.NoError(t, err)
			mapping := model.SSHDeviceSession{Original: originalName, Session: deviceName}
			config := model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}, DeviceSessions: []model.SSHDeviceSession{mapping}}}
			sh := sshWindowPrivateTransport(t, s, s.socket)
			snapshot := func() string {
				return s.must("-u", "list-sessions", "-F", "#{session_id}|#{session_name}|#{session_group}|#{session_windows}|#{window_id}") + "\n" +
					s.must("list-panes", "-a", "-F", "#{session_id}|#{window_id}|#{pane_id}|#{pane_active}") + "\n" +
					s.must("list-clients", "-F", "#{client_name}|#{session_id}")
			}
			before := snapshot()
			group := s.must("-u", "display-message", "-p", "-t", original+":", "#{session_group}")
			fingerprint := s.must("-u", "display-message", "-p", "-t", original+":", "#{pid}:#{start_time}:#{socket_path}")

			relation, err := tmux.ValidateSSHDeviceSession(t.Context(), config, sh, mapping)

			require.NoError(t, err)
			require.Equal(t, tmux.SSHDeviceSessionRelation{
				Original: originalName, Session: deviceName, OriginalID: original, SessionID: device,
				Group: group, ServerFingerprint: fingerprint,
				Links: []tmux.SSHDeviceWindowLink{{ID: first, Index: 0}, {ID: second, Index: 1}, {ID: first, Index: 3}},
			}, relation)
			require.Equal(t, before, snapshot(), "validator cannot create/select/attach/change options")
			_, err = os.Stat(filepath.Join(s.dir, "MUST_NOT_RUN"))
			require.True(t, os.IsNotExist(err), "shell/format-sensitive identities must remain literal")
		})
	}
	s.t = t
}

func TestSSHDeviceSessionMissingAndUngroupedOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShell(t)
	s.must("new-session", "-d", "-s", "device", "--", "/bin/cat")
	sh := sshWindowPrivateTransport(t, s, s.socket)
	before := s.must("list-sessions", "-F", "#{session_id}|#{session_name}|#{session_group}|#{window_id}")
	for _, names := range [][2]string{{"missing", "device"}, {"unrelated", "missing"}, {"unrelated", "device"}, {"unrel", "device"}} {
		mapping := model.SSHDeviceSession{Original: "ssh/dev/" + names[0], Session: "ssh/dev/" + names[1]}
		config := model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}, DeviceSessions: []model.SSHDeviceSession{mapping}}}
		relation, err := tmux.ValidateSSHDeviceSession(t.Context(), config, sh, mapping)
		require.Error(t, err)
		require.Zero(t, relation)
		require.Equal(t, before, s.must("list-sessions", "-F", "#{session_id}|#{session_name}|#{session_group}|#{window_id}"))
	}
	absent := filepath.Join(s.dir, "absent-server")
	sh = sshWindowPrivateTransport(t, s, absent)
	mapping := model.SSHDeviceSession{Original: "ssh/dev/unrelated", Session: "ssh/dev/device"}
	relation, err := tmux.ValidateSSHDeviceSession(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}, DeviceSessions: []model.SSHDeviceSession{mapping}}}, sh, mapping)
	require.Error(t, err)
	require.Zero(t, relation)
	_, err = os.Stat(absent)
	require.True(t, os.IsNotExist(err), "-N must not start an absent server")
	require.Equal(t, before, s.must("list-sessions", "-F", "#{session_id}|#{session_name}|#{session_group}|#{window_id}"))
}

func TestSSHDeviceSessionProcessCleanup(t *testing.T) {
	mapping := model.SSHDeviceSession{Original: "ssh/dev/work", Session: "ssh/dev/device"}
	config := model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}, DeviceSessions: []model.SSHDeviceSession{mapping}}}
	testSSHInventoryProcessCleanup(t, func(ctx context.Context, sh shell.Shell) error {
		relation, err := tmux.ValidateSSHDeviceSession(ctx, config, sh, mapping)
		if !reflect.DeepEqual(relation, tmux.SSHDeviceSessionRelation{}) {
			return errors.New("failed process returned authoritative relation metadata")
		}
		return err
	})
}

func TestSSHDeviceSessionDocumentsCustomHookEffects(t *testing.T) {
	s := newInventoryPrivateShell(t)
	s.must("set-option", "-g", "default-command", "exec /bin/cat")
	device := s.must("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "device", "-t", "$0")
	s.must("set-hook", "-t", "$0", "after-list-windows", "set-option -t '$0' @ticket58-relation-hook fired")
	sh := sshWindowPrivateTransport(t, s, s.socket)
	mapping := model.SSHDeviceSession{Original: "ssh/dev/unrelated", Session: "ssh/dev/device"}
	relation, err := tmux.ValidateSSHDeviceSession(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}, DeviceSessions: []model.SSHDeviceSession{mapping}}}, sh, mapping)
	require.NoError(t, err)
	require.Equal(t, "$0", relation.OriginalID)
	require.Equal(t, device, relation.SessionID)
	require.Equal(t, "fired", s.must("show-options", "-v", "-t", "$0", "@ticket58-relation-hook"), "observation commands do not suppress user hooks")
}
