package tmux

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/joshmedeski/sesh/v2/convert"
	"github.com/joshmedeski/sesh/v2/model"
)

func (t *RealTmux) ListSessions() ([]*model.TmuxSession, error) {
	output, err := t.captureInventory("-u", "list-sessions", "-F", listsessionsformat())
	if err != nil {
		return nil, err
	}
	sessions, err := parseTmuxSessionsOutput(output)
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		marker, err := t.GetSessionEnvironment(session.ID, model.SSHBridgeIdentityEnv)
		if err != nil {
			return nil, fmt.Errorf("failed to read SSH bridge marker for session %q: %w", session.ID, err)
		}
		session.SSHBridgeIdentity = marker
	}
	sortedSessions := sortByLastAttached(sessions)
	return sortedSessions, nil
}

var separator = "::"

var sessionInventorySchema = inventorySchema{
	tag: "S1",
	fields: []string{
		"#{session_activity}",
		`"#{q:session_alerts}"`,
		"#{session_attached}",
		`"#{q:session_attached_list}"`,
		"#{session_created}",
		"#{session_format}",
		`"#{q:session_group}"`,
		"#{session_group_attached}",
		`"#{q:session_group_attached_list}"`,
		`"#{q:session_group_list}"`,
		"#{session_group_many_attached}",
		"#{session_group_size}",
		"#{session_grouped}",
		"#{session_id}",
		"#{session_last_attached}",
		"#{session_many_attached}",
		"#{session_marked}",
		`"#{q:session_name}"`,
		`"#{q:session_path}"`,
		`"#{q:session_stack}"`,
		"#{session_windows}",
		inventoryGenerationFormat,
	},
}

func listsessionsformat() string {
	return sessionInventorySchema.format()
}

func parseTmuxSessionsOutput(output string) ([]*model.TmuxSession, error) {
	records, err := sessionInventorySchema.decode(output)
	if err != nil {
		return nil, err
	}
	sessions := make([]*model.TmuxSession, 0, len(records))
	for _, fields := range records {
		if !validTmuxID(fields[13], "$") {
			return nil, errors.New("unverified tmux session inventory identity")
		}
		for _, i := range []int{0, 2, 4, 7, 11, 14, 20} {
			if fields[i] == "" {
				continue
			}
			number, err := strconv.ParseInt(fields[i], 10, 64)
			if err != nil || number < 0 || strconv.FormatInt(number, 10) != fields[i] {
				return nil, errors.New("malformed tmux session inventory number")
			}
			if i == 2 || i == 7 || i == 11 || i == 20 {
				if _, err := strconv.Atoi(fields[i]); err != nil {
					return nil, errors.New("malformed tmux session inventory number")
				}
			}
		}
		for _, i := range []int{5, 10, 12, 15, 16} {
			if fields[i] != "" && fields[i] != "0" && fields[i] != "1" {
				return nil, errors.New("malformed tmux session inventory flag")
			}
		}
		if !validServerGeneration(fields[21]) {
			return nil, errors.New("unverified tmux session inventory generation")
		}

		session := &model.TmuxSession{
			Activity:          convert.StringToTime(fields[0]),
			Alerts:            splitNonEmpty(fields[1]),
			Attached:          convert.StringToInt(fields[2]),
			AttachedList:      strings.Split(fields[3], ","),
			Created:           convert.StringToTime(fields[4]),
			Format:            convert.StringToBool(fields[5]),
			Group:             fields[6],
			GroupAttached:     convert.StringToInt(fields[7]),
			GroupAttachedList: strings.Split(fields[8], ","),
			GroupList:         strings.Split(fields[9], ","),
			GroupManyAttached: convert.StringToBool(fields[10]),
			GroupSize:         convert.StringToInt(fields[11]),
			Grouped:           convert.StringToBool(fields[12]),
			ID:                fields[13],
			ServerGeneration:  fields[21],
			LastAttached:      convert.StringToTime(fields[14]),
			ManyAttached:      convert.StringToBool(fields[15]),
			Marked:            convert.StringToBool(fields[16]),
			Name:              fields[17],
			Path:              fields[18],
			Stack:             convert.StringToIntSlice(fields[19]),
			Windows:           convert.StringToInt(fields[20]),
		}
		sessions = append(sessions, session)
	}

	return sessions, nil
}

// splitNonEmpty splits a comma-separated alert string, dropping empty entries
// (tmux emits empty alert fields as bare commas).
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sortByLastAttached(sessions []*model.TmuxSession) []*model.TmuxSession {
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[j].LastAttached.Before(*sessions[i].LastAttached)
	})
	return sessions
}
