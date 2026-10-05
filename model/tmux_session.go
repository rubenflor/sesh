package model

import "time"

// SSHBridgeIdentityEnv holds the canonical SSH identity in a bridge's session environment.
const SSHBridgeIdentityEnv = "SESH_SSH_IDENTITY"

type TmuxSession struct {
	Created           *time.Time
	LastAttached      *time.Time
	Activity          *time.Time
	Group             string
	Path              string
	Name              string
	ID                string
	SSHBridgeIdentity string
	AttachedList      []string
	GroupList         []string
	GroupAttachedList []string
	Stack             []int
	Alerts            []string
	GroupSize         int
	GroupAttached     int
	Attached          int
	Windows           int
	Format            bool
	GroupManyAttached bool
	Grouped           bool
	ManyAttached      bool
	Marked            bool
}
