package model

import (
	"fmt"
	"slices"
	"strings"
)

// ValidateSSHDeviceSessions checks local declarations without contacting SSH.
func ValidateSSHDeviceSessions(config SSHConfig) error {
	seen := make(map[string]int, 2*len(config.DeviceSessions))
	for i, mapping := range config.DeviceSessions {
		if mapping.Original == "" || mapping.Session == "" {
			return fmt.Errorf("ssh.device_sessions entry %d requires original and session", i+1)
		}
		var hosts [2]string
		for j, field := range []struct{ name, identity string }{{"original", mapping.Original}, {"session", mapping.Session}} {
			host, session, err := ParseSSHIdentity(field.identity)
			if err != nil {
				return fmt.Errorf("ssh.device_sessions entry %d %s: %w", i+1, field.name, err)
			}
			if strings.ContainsRune(host, '\x00') || strings.ContainsRune(session, '\x00') {
				return fmt.Errorf("ssh.device_sessions entry %d %s contains NUL", i+1, field.name)
			}
			hosts[j] = host
		}
		if hosts[0] != hosts[1] {
			return fmt.Errorf("ssh.device_sessions entry %d must use the same host", i+1)
		}
		if !slices.Contains(config.Hosts, hosts[0]) {
			return fmt.Errorf("ssh.device_sessions entry %d host %q is not configured in [ssh].hosts", i+1, hosts[0])
		}
		if mapping.Original == mapping.Session {
			return fmt.Errorf("ssh.device_sessions entry %d requires different original and session identities", i+1)
		}
		for _, identity := range []string{mapping.Original, mapping.Session} {
			if owner, exists := seen[identity]; exists {
				return fmt.Errorf("ssh.device_sessions entry %d conflicts with entry %d for %q", i+1, owner, identity)
			}
			seen[identity] = i + 1
		}
	}
	return nil
}
