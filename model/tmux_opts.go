package model

// TmuxSessionOpts are the arguments for detached `tmux new-session` creation.
type TmuxSessionOpts struct {
	Name     string
	StartDir string
	// Command is the initial pane's shell command. Empty starts the default shell.
	Command string
	// Environment contains creation-time session assignments in KEY=VALUE form.
	Environment []string
	// Options require a non-empty Command and are applied before it starts,
	// without changing globals.
	SessionOptions []TmuxOption
	PaneOptions    []TmuxOption
}

type TmuxOption struct {
	Name  string
	Value string
}

// TmuxWindowOpts are the arguments for `tmux new-window`.
type TmuxWindowOpts struct {
	Name          string
	StartDir      string
	TargetSession string
	// Command becomes the new pane's process, so the window closes when the
	// command exits. Empty starts the default shell.
	Command    string
	Background bool // -d: create the window without making it the active one
}
