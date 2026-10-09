package tmux

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func sessionInventoryRow(fields []string) string {
	return inventoryRow(sessionInventorySchema, fields)
}

func sessionInventoryFields() []string {
	return []string{"1714092246", "", "0", "", "1714089765", "1", "", "", "", "", "", "", "0", "$1", "1714092246", "0", "0", "normal", "/tmp", "2,1", "2", "101:1700000000:/tmp/tmux-fixture"}
}

func TestListSessionsIncludesBridgeMetadata(t *testing.T) {
	s := shell.NewMockShell(t)
	tm := &RealTmux{shell: s, bin: "tmux"}
	s.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-sessions", "-F", mock.MatchedBy(func(format string) bool {
		return !strings.Contains(format, "#{SESH_SSH_IDENTITY}") && strings.HasSuffix(format, inventoryGenerationFormat)
	})).Return(rawInventoryResult(strings.Join([]string{
		sessionInventoryRow(strings.SplitN("1714092246::::0::::1714089765::1::::::::::::::0::$1::1714092246::0::0::renamed-bridge::/tmp::2,1::2::101:1700000000:/tmp/tmux::fixture", "::", 22)),
		sessionInventoryRow(strings.SplitN("1714092246::::0::::1714089765::1::::::::::::::0::$2::1714092245::0::0::sesh-ssh-user::/tmp::2,1::2::101:1700000000:/tmp/tmux::fixture", "::", 22)),
	}, "\n")), nil).Once()
	s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$1", "--", model.SSHBridgeIdentityEnv).
		Return("SESH_SSH_IDENTITY=ssh/host/work%2F1", nil).Once()
	s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$2", "--", model.SSHBridgeIdentityEnv).
		Return("", environmentExitError(t)).Once()
	s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$2").Return("OTHER=readable", nil).Once()

	sessions, err := tm.ListSessions()

	require.NoError(t, err)
	require.Len(t, sessions, 2)
	assert.Equal(t, "renamed-bridge", sessions[0].Name)
	assert.Equal(t, "ssh/host/work%2F1", sessions[0].SSHBridgeIdentity)
	assert.Equal(t, "sesh-ssh-user", sessions[1].Name)
	assert.Empty(t, sessions[1].SSHBridgeIdentity)
	assert.Equal(t, "101:1700000000:/tmp/tmux::fixture", sessions[0].ServerGeneration)
	assert.Equal(t, sessions[0].ServerGeneration, sessions[1].ServerGeneration)
}

func TestSessionGenerationMetadataFailsClosed(t *testing.T) {
	for _, generation := range []string{"", "101::/tmp/socket", ":1700000000:/tmp/socket", "101:1700000000:", "#{pid}:#{start_time}:#{socket_path}"} {
		t.Run(generation, func(t *testing.T) {
			fields := sessionInventoryFields()
			fields[21] = generation
			sessions, err := parseTmuxSessionsOutput(sessionInventoryRow(fields))
			require.Error(t, err)
			require.Nil(t, sessions)
		})
	}
}

func TestSessionInventoryPreservesAllPublicMetadata(t *testing.T) {
	fields := []string{"1714092246", "1:activity,2:bell", "2", "client::one,client\t雪", "1714089765", "1", `group::"\雪`, "3", "attached\nclient,other", "session::one,$0", "1", "4", "1", "$1", "1714092245", "1", "1", `name::"\雪`, "/tmp/space ::\n\t雪", "2,1", "5", "101:1700000000:/tmp/socket::\t雪"}
	sessions, err := parseTmuxSessionsOutput(sessionInventoryRow(fields))
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	activity := time.Unix(1714092246, 0)
	created := time.Unix(1714089765, 0)
	lastAttached := time.Unix(1714092245, 0)
	require.Equal(t, &model.TmuxSession{
		Activity: &activity, Alerts: []string{"1:activity", "2:bell"}, Attached: 2,
		AttachedList: []string{"client::one", "client\t雪"}, Created: &created, Format: true,
		Group: fields[6], GroupAttached: 3, GroupAttachedList: []string{"attached\nclient", "other"},
		GroupList: []string{"session::one", "$0"}, GroupManyAttached: true, GroupSize: 4, Grouped: true,
		ID: "$1", ServerGeneration: fields[21], LastAttached: &lastAttached, ManyAttached: true,
		Marked: true, Name: fields[17], Path: fields[18], Stack: []int{2, 1}, Windows: 5,
	}, sessions[0])
}

func TestListSessionsPropagatesBridgeMarkerLookupFailure(t *testing.T) {
	wantErr := errors.New("session disappeared")
	s := shell.NewMockShell(t)
	s.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-sessions", "-F", mock.Anything).Return(rawInventoryResult(sessionInventoryRow(sessionInventoryFields())), nil).Once()
	s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$1", "--", model.SSHBridgeIdentityEnv).
		Return("", wantErr).Once()

	sessions, err := NewTmux(nil, s, "tmux").ListSessions()

	require.ErrorIs(t, err, wantErr)
	require.ErrorContains(t, err, "$1")
	assert.Empty(t, sessions)
}

func TestListSessions(t *testing.T) {
	t.Run("List tmux session", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "tmux"}
		mockShell.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-sessions", "-F", mock.Anything).Return(rawInventoryResult(sessionInventoryRow(strings.Split("1714092246::::0::::1714089765::1::::::::::::::0::$1::1714092246::0::0::sesh/main::/Users/joshmedeski/c/sesh/main::2,1::2::101:1700000000:/tmp/tmux-fixture", "::"))),
			nil,
		)
		mockShell.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$1", "--", model.SSHBridgeIdentityEnv).
			Return("", nil).Once()
		sessions, err := tmux.ListSessions()
		assert.Nil(t, err)
		require.Len(t, sessions, 1)
		for _, session := range sessions {
			assert.Equal(t, "sesh/main", session.Name)
			assert.Equal(t, "/Users/joshmedeski/c/sesh/main", session.Path)
		}
	})

	t.Run("parseTmuxSessionsOutput", func(t *testing.T) {
		rawSessions := sessionInventoryRow(strings.Split("1714092246::::0::::1714089765::1::::::::::::::0::$1::1714092246::0::0::sesh/main::/Users/joshmedeski/c/sesh/main::2,1::2::101:1700000000:/tmp/tmux-fixture", "::"))
		sessions, err := parseTmuxSessionsOutput(rawSessions)
		assert.Nil(t, err)
		require.Len(t, sessions, 1)

		expectedName := "sesh/main"
		expectedPath := "/Users/joshmedeski/c/sesh/main"

		for _, session := range sessions {
			assert.Equal(t, expectedName, session.Name)
			assert.Equal(t, expectedPath, session.Path)
		}
	})

	t.Run("sortByLastAttached", func(t *testing.T) {
		const timeFormat = "2006-01-02 15:04:05 -0700 MST"
		createdFA, _ := time.Parse(timeFormat, "2024-04-25 19:02:45 -0500 CDT")
		lastAttachedFA, _ := time.Parse(timeFormat, "2024-04-25 19:30:06 -0500 CDT")
		activityFA, _ := time.Parse(timeFormat, "2024-04-25 19:44:06 -0500 CDT")
		firstAttached := model.TmuxSession{
			Created:           &createdFA,
			LastAttached:      &lastAttachedFA,
			Activity:          &activityFA,
			Group:             "",
			Path:              "/Users/joshmedeski/c/sesh/main",
			Name:              "sesh/main",
			ID:                "$1",
			AttachedList:      []string{""},
			GroupList:         []string{""},
			GroupAttachedList: []string{""},
			Stack:             []int{2, 1},
			Alerts:            []string{},
			GroupSize:         0,
			GroupAttached:     0,
			Attached:          0,
			Windows:           2,
			Format:            true,
			GroupManyAttached: false,
			Grouped:           false,
			ManyAttached:      false,
			Marked:            false,
		}

		createdLA, _ := time.Parse(timeFormat, "2024-04-25 19:02:45 -0500 CDT")
		lastAttachedLA, _ := time.Parse(timeFormat, "2024-04-25 19:44:06 -0500 CDT")
		activityLA, _ := time.Parse(timeFormat, "2024-04-25 19:44:06 -0500 CDT")
		lastAttached := model.TmuxSession{
			Created:           &createdLA,
			LastAttached:      &lastAttachedLA,
			Activity:          &activityLA,
			Group:             "",
			Path:              "/Users/joshmedeski/c/sesh/main",
			Name:              "sesh/main",
			ID:                "$1",
			AttachedList:      []string{""},
			GroupList:         []string{""},
			GroupAttachedList: []string{""},
			Stack:             []int{2, 1},
			Alerts:            []string{},
			GroupSize:         0,
			GroupAttached:     0,
			Attached:          0,
			Windows:           2,
			Format:            true,
			GroupManyAttached: false,
			Grouped:           false,
			ManyAttached:      false,
			Marked:            false,
		}

		expectedSortedSessions := []*model.TmuxSession{&lastAttached, &firstAttached}
		actualSortedSessionsOutOfOrder := sortByLastAttached([]*model.TmuxSession{&firstAttached, &lastAttached})
		assert.Equal(t, expectedSortedSessions, actualSortedSessionsOutOfOrder)
		actualSortedSessionsInOrder := sortByLastAttached([]*model.TmuxSession{&lastAttached, &firstAttached})
		assert.Equal(t, expectedSortedSessions, actualSortedSessionsInOrder)
	})
}
