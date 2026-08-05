package claude

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
	"strings"

	"github.com/motoki317/agtlog/internal/model"
)

type Source struct {
	parser Parser
	roots  []string
}

func NewSource(parser Parser, roots []string) Source {
	return Source{parser: parser, roots: normalizeRoots(roots)}
}

func DefaultRoots(home, configDirs string) []string {
	var roots []string
	for _, dir := range strings.Split(configDirs, ",") {
		if dir = strings.TrimSpace(dir); dir != "" {
			roots = append(roots, filepath.Join(dir, "projects"))
		}
	}
	if len(roots) == 0 {
		roots = []string{
			filepath.Join(home, ".config", "claude", "projects"),
			filepath.Join(home, ".claude", "projects"),
		}
	}
	return normalizeRoots(roots)
}

func (s Source) Agent() model.AgentKind {
	return model.AgentClaude
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
			if entry.IsDir() && entry.Name() == "subagents" {
				return filepath.SkipDir
			}
			if !entry.IsDir() && filepath.Ext(path) == ".jsonl" && !isLegacyAgentFile(path) {
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

func (s Source) ParseWithDiagnostics(path string, report func(string, error)) (*model.Session, error) {
	return s.parser.ParseWithDiagnostics(path, report)
}

func (s Source) LoadEvents(ctx context.Context, session *model.Session) error {
	return s.parser.LoadEvents(ctx, session)
}

func (s Source) LoadNodeEvents(ctx context.Context, session *model.Session) error {
	return s.parser.LoadNodeEvents(ctx, session)
}

func (s Source) Fingerprint(path string) (string, error) {
	hash := sha256.New()
	_, _ = fmt.Fprintln(hash, s.parser.CacheFingerprint())
	paths := []string{path, strings.TrimSuffix(path, filepath.Ext(path))}
	legacyPaths, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "agent-*.jsonl"))
	paths = append(paths, legacyPaths...)
	for _, candidate := range paths {
		err := filepath.Walk(candidate, func(current string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\n", current, info.ModTime().UnixNano(), info.Size())
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s Source) AffectedPath(path string) string {
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "subagents" {
			return filepath.Dir(dir) + ".jsonl"
		}
	}
	if isLegacyAgentFile(path) {
		parentID := sessionIDFromFile(path)
		candidates, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.jsonl"))
		for _, candidate := range candidates {
			if !isLegacyAgentFile(candidate) && sessionIDFromFile(candidate) == parentID {
				return candidate
			}
		}
	}
	return path
}

func isLegacyAgentFile(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, "agent-") && filepath.Ext(base) == ".jsonl"
}
