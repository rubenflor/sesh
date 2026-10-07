package model

type TmuxPane struct {
	WindowIndex       int
	WindowName        string
	PaneIndex         int
	PaneTitle         string
	PaneCommand       string
	PanePath          string
	PaneID            string
	SessionID         string
	WindowID          string
	ServerGeneration  string
	SSHBridgeIdentity string
}
