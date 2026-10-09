package model

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const sshIdentityHex = "0123456789ABCDEF"

// TmuxSSHSessionTarget matches tmux's stored vis-escaped session name; the
// canonical SSH identity carries the original bytes instead.
func TmuxSSHSessionTarget(session string) string {
	var target strings.Builder
	for i := 0; i < len(session); i++ {
		b := session[i]
		const controls = "\a\b\t\n\v\f\r\\"
		const escapes = "abtnvfr\\"
		if index := strings.IndexByte(controls, b); index >= 0 {
			target.WriteByte('\\')
			target.WriteByte(escapes[index])
		} else if b < 0x20 || b == 0x7f {
			fmt.Fprintf(&target, "\\%03o", b)
		} else {
			target.WriteByte(b)
		}
	}
	return target.String()
}

// EncodeSSHIdentity returns the canonical public identity for an SSH host and
// remote tmux session.
func EncodeSSHIdentity(host, session string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("SSH identity host must not be empty")
	}
	if session == "" {
		return "", fmt.Errorf("SSH identity session must not be empty")
	}
	if !utf8.ValidString(host) {
		return "", fmt.Errorf("SSH identity host must be valid UTF-8")
	}
	if !utf8.ValidString(session) {
		return "", fmt.Errorf("SSH identity session must be valid UTF-8")
	}

	return "ssh/" + encodeSSHIdentityComponent(host) + "/" + encodeSSHIdentityComponent(session), nil
}

// ParseSSHIdentity parses a canonical public SSH identity and returns its host
// alias and remote tmux session name.
func ParseSSHIdentity(identity string) (host, session string, err error) {
	parts := strings.Split(identity, "/")
	if len(parts) != 3 || parts[0] != "ssh" {
		return "", "", fmt.Errorf("invalid SSH identity %q: expected ssh/<host>/<session>", identity)
	}
	if parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("invalid SSH identity %q: host and session must not be empty", identity)
	}

	hostBytes, err := decodeSSHIdentityComponent(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("invalid SSH identity %q: host: %w", identity, err)
	}
	sessionBytes, err := decodeSSHIdentityComponent(parts[2])
	if err != nil {
		return "", "", fmt.Errorf("invalid SSH identity %q: session: %w", identity, err)
	}
	if !utf8.Valid(hostBytes) || !utf8.Valid(sessionBytes) {
		return "", "", fmt.Errorf("invalid SSH identity %q: components must be valid UTF-8", identity)
	}

	host, session = string(hostBytes), string(sessionBytes)
	canonical, err := EncodeSSHIdentity(host, session)
	if err != nil {
		return "", "", fmt.Errorf("invalid SSH identity %q: %w", identity, err)
	}
	if canonical != identity {
		return "", "", fmt.Errorf("invalid SSH identity %q: non-canonical encoding", identity)
	}

	return host, session, nil
}

func encodeSSHIdentityComponent(value string) string {
	var encoded strings.Builder
	encoded.Grow(len(value))
	for i := 0; i < len(value); i++ {
		b := value[i]
		if isSSHIdentityUnreserved(b) {
			encoded.WriteByte(b)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(sshIdentityHex[b>>4])
		encoded.WriteByte(sshIdentityHex[b&0x0f])
	}
	return encoded.String()
}

func decodeSSHIdentityComponent(value string) ([]byte, error) {
	decoded := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			decoded = append(decoded, value[i])
			continue
		}
		if i+2 >= len(value) {
			return nil, fmt.Errorf("malformed percent escape")
		}
		high, ok := sshIdentityHexValue(value[i+1])
		if !ok {
			return nil, fmt.Errorf("malformed percent escape")
		}
		low, ok := sshIdentityHexValue(value[i+2])
		if !ok {
			return nil, fmt.Errorf("malformed percent escape")
		}
		decoded = append(decoded, high<<4|low)
		i += 2
	}
	return decoded, nil
}

func isSSHIdentityUnreserved(b byte) bool {
	return b >= 'a' && b <= 'z' ||
		b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' ||
		b == '-' || b == '.' || b == '_' || b == '~'
}

func sshIdentityHexValue(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	default:
		return 0, false
	}
}
