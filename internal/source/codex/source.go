package codex

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/motoki317/agtlog/internal/model"
)

type Source struct {
	parser Parser
	roots  []string
}

func NewSource(parser Parser, roots []string) Source {
	return Source{parser: parser, roots: roots}
}

func DefaultRoots(home, codexHome string) []string {
	if codexHome != "" {
		return []string{filepath.Join(codexHome, "sessions")}
	}
	return []string{filepath.Join(home, ".codex", "sessions")}
}

func (s Source) Agent() model.AgentKind {
	return model.AgentCodex
}

func (s Source) Roots() []string {
	return append([]string(nil), s.roots...)
}

func (s Source) Discover(ctx context.Context) ([]string, error) {
	var paths []string
	for _, root := range s.roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !entry.IsDir() && filepath.Ext(path) == ".jsonl" {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (s Source) Parse(path string) (*model.Session, error) {
	return s.parser.Parse(path)
}
