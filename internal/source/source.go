package source

import (
	"context"

	"github.com/motoki317/agtlog/internal/model"
)

type Source interface {
	Agent() model.AgentKind
	Roots() []string
	CacheFingerprint() string
	Discover(context.Context) ([]string, error)
	ParseContext(context.Context, string) (*model.Session, error)
	Reprice(*model.Session)
}
