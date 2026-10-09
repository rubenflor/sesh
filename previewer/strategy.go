package previewer

import "context"

type PreviewStrategy interface {
	Execute(name string) (string, error)
}

type ContextPreviewStrategy interface {
	ExecuteContext(context.Context, string) (string, error)
}

// ExclusivePreviewStrategy claims inputs that must not fall through to later
// strategies after execution, including when the output is empty.
type ExclusivePreviewStrategy interface {
	Handles(string) bool
}
