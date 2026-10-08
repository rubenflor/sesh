package model

type TmuxWindow struct {
	Name   string
	Path   string
	Index  int
	Active bool

	// Operational identity stays internal; index/name remain display metadata.
	ID               string `json:"-"`
	ServerGeneration string `json:"-"`
}
