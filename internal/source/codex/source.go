package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	return Source{parser: parser, roots: normalizeRoots(roots)}
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
	seen := make(map[string]bool)
	for _, root := range s.roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !entry.IsDir() && filepath.Ext(path) == ".jsonl" {
				info, infoErr := entry.Info()
				if infoErr != nil {
					return infoErr
				}
				if info.Mode().IsRegular() {
					seen[path] = true
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func normalizeRoots(roots []string) []string {
	seen := make(map[string]bool)
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		root = filepath.Clean(root)
		if canonical, err := filepath.EvalSymlinks(root); err == nil {
			root = canonical
		}
		if !seen[root] {
			seen[root] = true
			normalized = append(normalized, root)
		}
	}
	return normalized
}

func (s Source) Parse(path string) (*model.Session, error) {
	return s.parser.Parse(path)
}

func (s Source) Fingerprint(path string) (string, error) {
	fingerprint, err := fileFingerprint(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s", s.parser.CacheFingerprint(), fingerprint)))
	return hex.EncodeToString(digest[:]), nil
}

func fileFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("session path is not a regular file")
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()), nil
}
