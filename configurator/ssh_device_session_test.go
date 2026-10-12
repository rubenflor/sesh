package configurator

import (
	"os"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/pathwrap"
	"github.com/joshmedeski/sesh/v2/runtimewrap"
	"github.com/stretchr/testify/require"
	"github.com/xeipuuv/gojsonschema"
)

func TestGetConfigSSHDeviceSessions(t *testing.T) {
	config, err := configFromTOML(t, `strict_mode = true
[ssh]
hosts = ["myinventa"]
[[ssh.device_sessions]]
original = "ssh/myinventa/1"
session = "ssh/myinventa/1-macbook"
`)
	require.NoError(t, err)
	require.Equal(t, []model.SSHDeviceSession{{Original: "ssh/myinventa/1", Session: "ssh/myinventa/1-macbook"}}, config.SSH.DeviceSessions)
}

func TestGetConfigSSHDeviceSessionsRejectMissingField(t *testing.T) {
	for _, fields := range []string{"original = 'ssh/dev/work'", "session = 'ssh/dev/device'", "original = ''\nsession = 'ssh/dev/device'", "original = 'ssh/dev/work'\nsession = ''"} {
		_, err := configFromTOML(t, "[ssh]\nhosts = ['dev']\n[[ssh.device_sessions]]\n"+fields)
		require.ErrorContains(t, err, "original and session")
	}
}

func TestGetConfigSSHDeviceSessionsDefaultsAndLiteralNames(t *testing.T) {
	for _, raw := range []string{"", "[ssh]\nhosts = []", "[ssh]\nhosts = ['dev']\ndevice_sessions = []"} {
		config, err := configFromTOML(t, raw)
		require.NoError(t, err)
		require.Empty(t, config.SSH.DeviceSessions)
		require.Equal(t, "top", config.TUI.InputPosition)
		require.Equal(t, "default", config.TUI.Keymap)
	}
	const original = "ssh/dev%2F%E9%9B%AA%25/a%2Fb%25%20caf%C3%A9%E9%9B%AA%20%24HOME"
	const device = "ssh/dev%2F%E9%9B%AA%25/-mac%5Cbook%3A%20%27%22%23%7Bsession_id%7D"
	config, err := configFromTOML(t, "strict_mode = true\n[ssh]\nhosts = ['dev/雪%']\n[[ssh.device_sessions]]\noriginal = '"+original+"'\nsession = '"+device+"'\n")
	require.NoError(t, err)
	require.Equal(t, []model.SSHDeviceSession{{Original: original, Session: device}}, config.SSH.DeviceSessions)
}

func TestGetConfigSSHDeviceSessionsRootOnlyAndDefaultPath(t *testing.T) {
	root := "[ssh]\nhosts = ['dev']\n[[ssh.device_sessions]]\noriginal = 'ssh/dev/work'\nsession = 'ssh/dev/device'\n"
	imported := "[ssh]\nhosts = ['other']\n[[ssh.device_sessions]]\noriginal = 'ssh/other/work'\nsession = 'ssh/other/device'\n"
	mockOs := &testOs{homeDir: "/home/testuser", files: map[string][]byte{
		"/home/testuser/.config/sesh/sesh.toml": []byte("import = ['/import.toml']\n" + root),
		"/import.toml":                          []byte(imported),
	}}
	config, err := NewConfigurator(mockOs, pathwrap.NewPath(), &runtimewrap.MockRunTime{}).GetConfig()
	require.NoError(t, err)
	require.Equal(t, []model.SSHDeviceSession{{Original: "ssh/dev/work", Session: "ssh/dev/device"}}, config.SSH.DeviceSessions)
	mockOs.files["/home/testuser/.config/sesh/sesh.toml"] = []byte("[ssh]\nhosts = ['dev']\n[[ssh.device_sessions]]\noriginal = 'ssh/dev/work'")
	_, err = NewConfigurator(mockOs, pathwrap.NewPath(), &runtimewrap.MockRunTime{}).GetConfig()
	require.ErrorContains(t, err, "original and session")
}

func TestGetConfigSSHDeviceSessionsReadmeAndMultipleMappings(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	require.NoError(t, err)
	_, section, found := strings.Cut(string(readme), "#### Persistent per-device grouped sessions\n")
	require.True(t, found)
	_, example, found := strings.Cut(section, "```toml\n")
	require.True(t, found)
	example, _, found = strings.Cut(example, "```")
	require.True(t, found)
	config, err := configFromTOML(t, "strict_mode = true\n"+example)
	require.NoError(t, err)
	require.Equal(t, []model.SSHDeviceSession{{Original: "ssh/myinventa/1", Session: "ssh/myinventa/1-macbook"}}, config.SSH.DeviceSessions)
	config, err = configFromTOML(t, example+"\n[[ssh.device_sessions]]\noriginal = 'ssh/myinventa/work'\nsession = 'ssh/myinventa/work-macbook'\n")
	require.NoError(t, err)
	require.Len(t, config.SSH.DeviceSessions, 2)
	require.Equal(t, "ssh/myinventa/1", config.SSH.DeviceSessions[0].Original)
	require.Equal(t, "ssh/myinventa/work", config.SSH.DeviceSessions[1].Original)
}

func TestGetConfigSSHDeviceSessionsSchema(t *testing.T) {
	data, err := os.ReadFile("../sesh.schema.json")
	require.NoError(t, err)
	for _, test := range []struct{ name, raw string }{
		{"valid_mapping", `{"ssh":{"hosts":["dev"],"device_sessions":[{"original":"ssh/dev/work","session":"ssh/dev/device"}]}}`},
		{"empty_mappings", `{"ssh":{"hosts":[],"device_sessions":[]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := gojsonschema.Validate(gojsonschema.NewBytesLoader(data), gojsonschema.NewStringLoader(test.raw))
			require.NoError(t, err)
			require.True(t, result.Valid(), "%v", result.Errors())
		})
	}
	for _, test := range []struct{ name, fields, errorType string }{
		{"missing_session", `{"original":"ssh/dev/work"}`, ""},
		{"missing_original", `{"session":"ssh/dev/device"}`, ""},
		{"local_identity", `{"original":"local","session":"ssh/dev/device"}`, ""},
		{"noncanonical_identity", `{"original":"ssh/dev/a%2fb","session":"ssh/dev/device"}`, ""},
		{"empty_session", `{"original":"ssh/dev/work","session":""}`, "pattern"},
		{"extra_property", `{"original":"ssh/dev/work","session":"ssh/dev/device","extra":true}`, "additional_property_not_allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := gojsonschema.Validate(gojsonschema.NewBytesLoader(data), gojsonschema.NewStringLoader(`{"ssh":{"hosts":["dev"],"device_sessions":[`+test.fields+`]}}`))
			require.NoError(t, err)
			require.False(t, result.Valid(), test.fields)
			if test.errorType != "" {
				require.Len(t, result.Errors(), 1)
				require.Equal(t, test.errorType, result.Errors()[0].Type())
			}
		})
	}
}

func TestGetConfigSSHDeviceSessionsRejectInvalidIdentities(t *testing.T) {
	for _, identity := range []string{"local", "ssh/dev", "ssh//work", "ssh/dev/", "ssh/dev/a/b", "ssh/dev/%zz", "ssh/dev/a%2fb", "ssh/dev/%77ork", "ssh/dev/%FF", "ssh/dev/%00"} {
		t.Run(identity, func(t *testing.T) {
			_, err := configFromTOML(t, "[ssh]\nhosts = ['dev']\n[[ssh.device_sessions]]\noriginal = '"+identity+"'\nsession = 'ssh/dev/device'\n")
			require.ErrorContains(t, err, "original")
			_, err = configFromTOML(t, "[ssh]\nhosts = ['dev']\n[[ssh.device_sessions]]\noriginal = 'ssh/dev/work'\nsession = '"+identity+"'\n")
			require.ErrorContains(t, err, "session")
		})
	}
}

func TestGetConfigSSHDeviceSessionsRejectHostAndMappingConflicts(t *testing.T) {
	for _, test := range []struct{ name, mappings, want string }{
		{"unconfigured", "original = 'ssh/other/work'\nsession = 'ssh/other/device'", "configured"},
		{"different hosts", "original = 'ssh/dev/work'\nsession = 'ssh/other/device'", "same host"},
		{"same session", "original = 'ssh/dev/work'\nsession = 'ssh/dev/work'", "different"},
		{"duplicate", "original = 'ssh/dev/work'\nsession = 'ssh/dev/device'\n[[ssh.device_sessions]]\noriginal = 'ssh/dev/work'\nsession = 'ssh/dev/device'", "conflict"},
		{"original reused", "original = 'ssh/dev/work'\nsession = 'ssh/dev/device'\n[[ssh.device_sessions]]\noriginal = 'ssh/dev/work'\nsession = 'ssh/dev/second-device'", "conflict"},
		{"device reused", "original = 'ssh/dev/work'\nsession = 'ssh/dev/device'\n[[ssh.device_sessions]]\noriginal = 'ssh/dev/other-work'\nsession = 'ssh/dev/device'", "conflict"},
		{"roles overlap", "original = 'ssh/dev/work'\nsession = 'ssh/dev/device'\n[[ssh.device_sessions]]\noriginal = 'ssh/dev/device'\nsession = 'ssh/dev/second-device'", "conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := "[ssh]\nhosts = ['dev', 'other']\n[[ssh.device_sessions]]\n"
			if test.name == "unconfigured" {
				header = "[ssh]\nhosts = ['dev']\n[[ssh.device_sessions]]\n"
			}
			_, err := configFromTOML(t, header+test.mappings)
			require.ErrorContains(t, err, test.want)
		})
	}
}
