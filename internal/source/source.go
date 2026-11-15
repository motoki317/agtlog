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

type resumableContextParser interface {
	Source
	ParseResumableContext(context.Context, string, any) (*model.Session, any, error)
}

type partContextParser interface {
	ParseWithPartsContext(context.Context, string, func(string, error), func(string) ([]byte, bool), func(string, []byte)) (*model.Session, error)
}
