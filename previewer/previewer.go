package previewer

import (
	"context"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/icon"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/ls"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
)

type Previewer interface {
	// Previews a session or directory
	Preview(name string) (string, error)
}

type ContextPreviewer interface {
	PreviewContext(context.Context, string) (string, error)
}

type RealPreviewer struct {
	icon       icon.Icon
	strategies []PreviewStrategy
}

func NewPreviewer(
	lister lister.Lister,
	tmux tmux.Tmux,
	icon icon.Icon,
	dir dir.Dir,
	home home.Home,
	ls ls.Ls,
	config model.Config,
	shell shell.Shell,
) Previewer {
	strategies := []PreviewStrategy{
		NewSSHStrategy(config, shell),
		NewTmuxStrategy(lister, tmux),
		NewConfigStrategy(lister, shell),
		NewDefaultConfigStrategy(lister, config, ls),
		NewDirectoryStrategy(home, dir, ls),
	}

	return &RealPreviewer{
		icon:       icon,
		strategies: strategies,
	}
}

func (p *RealPreviewer) Preview(name string) (string, error) {
	return p.PreviewContext(context.Background(), name)
}

func (p *RealPreviewer) PreviewContext(ctx context.Context, name string) (string, error) {
	trimmedName := p.icon.RemoveIcon(name)

	for _, strategy := range p.strategies {
		exclusive, isExclusive := strategy.(ExclusivePreviewStrategy)
		if isExclusive && !exclusive.Handles(trimmedName) {
			continue
		}

		var (
			output string
			err    error
		)
		if contextual, ok := strategy.(ContextPreviewStrategy); ok {
			output, err = contextual.ExecuteContext(ctx, trimmedName)
		} else {
			output, err = strategy.Execute(trimmedName)
		}
		if err != nil {
			return "", err
		}
		if isExclusive {
			return output, nil
		}
		if output != "" {
			return output, nil
		}
	}
	return "", nil
}
