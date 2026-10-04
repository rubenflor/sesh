package model

import "testing"

func TestEncodeSSHIdentity(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		session string
		want    string
	}{
		{
			name:    "plain host and session",
			host:    "myinventa",
			session: "0",
			want:    "ssh/myinventa/0",
		},
		{
			name:    "slashes are encoded within components",
			host:    "my/host",
			session: "nutiliti/2345",
			want:    "ssh/my%2Fhost/nutiliti%2F2345",
		},
		{
			name:    "percent is encoded",
			host:    "host%name",
			session: "session%1",
			want:    "ssh/host%25name/session%251",
		},
		{
			name:    "utf8 and spaces are encoded as bytes",
			host:    "höst",
			session: "café 東京",
			want:    "ssh/h%C3%B6st/caf%C3%A9%20%E6%9D%B1%E4%BA%AC",
		},
		{
			name:    "rfc3986 unreserved bytes remain literal",
			host:    "AZaz09-._~",
			session: "AZaz09-._~",
			want:    "ssh/AZaz09-._~/AZaz09-._~",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EncodeSSHIdentity(tt.host, tt.session)
			if err != nil {
				t.Fatalf("EncodeSSHIdentity() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("EncodeSSHIdentity() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEncodeSSHIdentityRejectsInvalidComponents(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		session string
	}{
		{name: "empty host", host: "", session: "0"},
		{name: "empty session", host: "host", session: ""},
		{name: "invalid utf8 host", host: string([]byte{0xff}), session: "0"},
		{name: "invalid utf8 session", host: "host", session: string([]byte{0xff})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := EncodeSSHIdentity(tt.host, tt.session); err == nil {
				t.Fatal("EncodeSSHIdentity() error = nil, want error")
			}
		})
	}
}

func TestParseSSHIdentity(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		host    string
		session string
	}{
		{
			name:    "plain host and session",
			input:   "ssh/myinventa/0",
			host:    "myinventa",
			session: "0",
		},
		{
			name:    "encoded slash stays within a component",
			input:   "ssh/my%2Fhost/nutiliti%2F2345",
			host:    "my/host",
			session: "nutiliti/2345",
		},
		{
			name:    "percent decoding happens once",
			input:   "ssh/host/session%252F",
			host:    "host",
			session: "session%2F",
		},
		{
			name:    "utf8 and spaces decode",
			input:   "ssh/h%C3%B6st/caf%C3%A9%20%E6%9D%B1%E4%BA%AC",
			host:    "höst",
			session: "café 東京",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, session, err := ParseSSHIdentity(tt.input)
			if err != nil {
				t.Fatalf("ParseSSHIdentity() error = %v", err)
			}
			if host != tt.host || session != tt.session {
				t.Errorf("ParseSSHIdentity() = (%q, %q), want (%q, %q)", host, session, tt.host, tt.session)
			}
		})
	}
}

func TestParseSSHIdentityRejectsInvalidAndNonCanonicalValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "wrong scheme", input: "tmux/host/0"},
		{name: "missing session", input: "ssh/host"},
		{name: "extra literal separator", input: "ssh/host/session/part"},
		{name: "empty host", input: "ssh//0"},
		{name: "empty session", input: "ssh/host/"},
		{name: "incomplete escape", input: "ssh/host/%2"},
		{name: "invalid escape", input: "ssh/host/%GG"},
		{name: "unescaped percent", input: "ssh/host/session%name"},
		{name: "lowercase percent escape", input: "ssh/host/a%2fb"},
		{name: "encoded unreserved byte", input: "ssh/host/%7E"},
		{name: "invalid utf8", input: "ssh/host/%FF"},
		{name: "raw space is not canonical", input: "ssh/host/session name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ParseSSHIdentity(tt.input); err == nil {
				t.Fatalf("ParseSSHIdentity(%q) error = nil, want error", tt.input)
			}
		})
	}
}
