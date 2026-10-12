package model

// SSHWindow is remote discovery metadata, not authorization to connect.
// Native IDs are server-scoped; ServerFingerprint is advisory, not a UUID.
type SSHWindow struct {
	Name              string
	Path              string
	Index             int
	Active            bool
	Src               string
	Session           string
	SessionID         string
	ID                string
	ServerFingerprint string
}
