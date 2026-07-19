package source

import (
	"context"

	"github.com/motoki317/agtlog/internal/model"
)

type Source interface {
	Agent() model.AgentKind
	Roots() []string
	Discover(context.Context) ([]string, error)
	Parse(string) (*model.Session, error)
}
