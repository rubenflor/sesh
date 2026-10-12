package tmux

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
)

// SSHDeviceWindowLink identifies a session-relative link, not a display name.
type SSHDeviceWindowLink struct {
	ID    string
	Index int
}

// SSHDeviceSessionRelation is consistent observation metadata only. It does not
// authorize a later connection; ServerFingerprint is advisory, not a generation.
type SSHDeviceSessionRelation struct {
	Original, Session     string
	OriginalID, SessionID string
	Group                 string
	Links                 []SSHDeviceWindowLink
	ServerFingerprint     string
}

var sshDeviceSessionSchema = inventorySchema{tag: "DG1", fields: []string{
	"W", "O", "#{session_id}", `"#{q:session_name}"`, `"#{q:session_group}"`,
	"#{window_id}", "#{window_index}", "#{session_windows}", inventoryGenerationFormat,
}}

func sshDeviceSessionFormat(kind, role string) string {
	fields := append([]string(nil), sshDeviceSessionSchema.fields...)
	fields[0], fields[1] = kind, role
	if kind != "W" {
		fields[5], fields[6] = "", ""
	}
	return (inventorySchema{tag: "DG1", fields: fields}).format()
}

// ValidateSSHDeviceSession explicitly observes one locally configured mapping.
// Nothing calls it at startup/list/preview. Only native enumeration is issued;
// arbitrary user-defined tmux after/error hooks may still have indirect effects.
func ValidateSSHDeviceSession(ctx context.Context, config model.Config, sh shell.Shell, mapping model.SSHDeviceSession) (SSHDeviceSessionRelation, error) {
	ctx, cancel := context.WithTimeout(ctx, sshWindowTimeout)
	defer cancel()
	if err := model.ValidateSSHDeviceSessions(config.SSH); err != nil {
		return SSHDeviceSessionRelation{}, err
	}
	if !slices.Contains(config.SSH.DeviceSessions, mapping) {
		return SSHDeviceSessionRelation{}, errors.New("SSH device session mapping is not configured in [ssh].device_sessions")
	}
	host, original, err := model.ParseSSHIdentity(mapping.Original)
	if err != nil {
		return SSHDeviceSessionRelation{}, err
	}
	_, device, err := model.ParseSSHIdentity(mapping.Session)
	if err != nil {
		return SSHDeviceSessionRelation{}, err
	}
	if err := ctx.Err(); err != nil {
		return SSHDeviceSessionRelation{}, err
	}
	remote := sshSessionLookupScript(original) + "original=$target\n" +
		sshSessionLookupScript(device) + "device=$target\n"
	for _, kind := range []string{"B", "W", "E"} {
		for i, target := range []string{"original", "device"} {
			role := []string{"O", "D"}[i]
			if kind == "W" {
				remote += "tmux -N -u list-windows -t \"$" + target + "\" -F "
			} else {
				remote += "tmux -N -u display-message -p -t \"${" + target + "}:\" "
			}
			remote += shell.Quote(sshDeviceSessionFormat(kind, role)) + " || exit $?\n"
		}
	}
	output, err := captureSSHInventory(ctx, sh, host, remote, "remote tmux device-session observation failed")
	if err != nil {
		return SSHDeviceSessionRelation{}, fmt.Errorf("inspect SSH device sessions %q and %q: %w", mapping.Original, mapping.Session, err)
	}
	relation, err := parseSSHDeviceSessionRelation(string(output), mapping, original, device)
	if err != nil {
		return SSHDeviceSessionRelation{}, fmt.Errorf("SSH %q: invalid device-session relation: %w", host, err)
	}
	if err := ctx.Err(); err != nil {
		return SSHDeviceSessionRelation{}, fmt.Errorf("SSH %q: %w", host, err)
	}
	return relation, nil
}

func parseSSHDeviceSessionRelation(output string, mapping model.SSHDeviceSession, original, device string) (SSHDeviceSessionRelation, error) {
	if !utf8.ValidString(output) || !strings.HasSuffix(output, "\n") {
		return SSHDeviceSessionRelation{}, errors.New("invalid UTF-8 or incomplete inventory")
	}
	records, err := sshDeviceSessionSchema.decode(output)
	if err != nil {
		return SSHDeviceSessionRelation{}, err
	}
	if len(records) < 4 {
		return SSHDeviceSessionRelation{}, errors.New("missing inventory boundaries")
	}
	counts := [2]int{}
	for i, name := range []string{original, device} {
		begin, end := records[i], records[len(records)-2+i]
		role := []string{"O", "D"}[i]
		if begin[0] != "B" || end[0] != "E" || begin[1] != role || !slices.Equal(begin[1:], end[1:]) ||
			!validTmuxID(begin[2], "$") || begin[3] != model.TmuxSSHSessionTarget(name) ||
			begin[5] != "" || begin[6] != "" || !validServerGeneration(begin[8]) {
			return SSHDeviceSessionRelation{}, errors.New("session, group or server changed during observation")
		}
		count, err := strconv.Atoi(begin[7])
		if err != nil || count < 0 || count > len(records) || strconv.Itoa(count) != begin[7] {
			return SSHDeviceSessionRelation{}, errors.New("invalid window count")
		}
		counts[i] = count
	}
	if records[0][2] == records[1][2] {
		return SSHDeviceSessionRelation{}, errors.New("original and device have the same native session ID")
	}
	if records[0][4] == "" || records[0][4] != records[1][4] {
		return SSHDeviceSessionRelation{}, errors.New("original and device must belong to the same nonempty session group")
	}
	if records[0][8] != records[1][8] {
		return SSHDeviceSessionRelation{}, errors.New("remote server observations disagree")
	}
	if counts[0] != counts[1] || counts[0]+counts[1]+4 != len(records) {
		return SSHDeviceSessionRelation{}, errors.New("incomplete or inconsistent window links")
	}
	links := make([]SSHDeviceWindowLink, 0, counts[1])
	indices := [2]map[int]string{}
	offset := 2
	for i, count := range counts {
		indices[i] = make(map[int]string, count)
		begin := records[i]
		for _, row := range records[offset : offset+count] {
			if row[0] != "W" || !slices.Equal(row[1:5], begin[1:5]) || row[7] != begin[7] || row[8] != begin[8] {
				return SSHDeviceSessionRelation{}, errors.New("unexpected parent, group or window record")
			}
			index, err := strconv.Atoi(row[6])
			if !validWindowID(row[5]) || err != nil || index < 0 || strconv.Itoa(index) != row[6] {
				return SSHDeviceSessionRelation{}, errors.New("invalid window ID or link index")
			}
			if _, exists := indices[i][index]; exists {
				return SSHDeviceSessionRelation{}, errors.New("duplicate window link index")
			}
			indices[i][index] = row[5]
			if i == 1 {
				links = append(links, SSHDeviceWindowLink{ID: row[5], Index: index})
			}
		}
		offset += count
	}
	if !maps.Equal(indices[0], indices[1]) {
		return SSHDeviceSessionRelation{}, errors.New("group members have different native window/index links")
	}
	return SSHDeviceSessionRelation{
		Original: mapping.Original, Session: mapping.Session,
		OriginalID: records[0][2], SessionID: records[1][2], Group: records[0][4],
		Links: links, ServerFingerprint: records[0][8],
	}, nil
}
