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

func Roots(home, configDir string, extra []string) []string {
	var roots []string
	if configDir != "" {
		roots = append(roots, filepath.Join(configDir, "projects"))
	} else {
		roots = []string{
			filepath.Join(home, ".config", "claude", "projects"),
			filepath.Join(home, ".claude", "projects"),
		}
	}
	for _, dir := range extra {
		roots = append(roots, filepath.Join(dir, "projects"))
	}
	return normalizeRoots(roots)
}

func (s Source) Agent() model.AgentKind {
	return model.AgentClaude
}

func (s Source) Roots() []string {
	return append([]string(nil), s.roots...)
}

func (s Source) CacheFingerprint() string {
	return s.parser.CacheFingerprint()
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
		key := root
		if absolute, err := filepath.Abs(root); err == nil {
			key = absolute
		}
		if !seen[key] {
			seen[key] = true
			normalized = append(normalized, root)
		}
	}
	return normalized
}

func (s Source) Parse(path string) (*model.Session, error) {
	return s.parser.Parse(path)
}

func (s Source) ParseContext(ctx context.Context, path string) (*model.Session, error) {
	return s.parser.ParseContext(ctx, path)
}

func (s Source) Reprice(session *model.Session) {
	s.parser.calculator.ApplySession(session)
}

func (s Source) ParseWithDiagnostics(path string, report func(string, error)) (*model.Session, error) {
	return s.parser.ParseWithDiagnostics(path, report)
}

func (s Source) ParseWithDiagnosticsContext(ctx context.Context, path string, report func(string, error)) (*model.Session, error) {
	return s.parser.ParseWithDiagnosticsContext(ctx, path, report)
}

func (s Source) LoadEvents(ctx context.Context, session *model.Session) error {
	return s.parser.LoadEvents(ctx, session)
}

func (s Source) LoadNodeEvents(ctx context.Context, session *model.Session) error {
	return s.parser.LoadNodeEvents(ctx, session)
}

func (s Source) Fingerprint(path string) (string, error) {
	return s.FingerprintContext(context.Background(), path)
}

func (s Source) FingerprintContext(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = fmt.Fprintln(hash, s.parser.CacheFingerprint())
	paths := []string{path, strings.TrimSuffix(path, filepath.Ext(path))}
	legacyPaths, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "agent-*.jsonl"))
	paths = append(paths, legacyPaths...)
	for _, candidate := range paths {
		err := filepath.Walk(candidate, func(current string, info os.FileInfo, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
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
	affected, _ := s.AffectedPathContext(context.Background(), path)
	return affected
}

func (s Source) AffectedPathContext(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "subagents" {
			return filepath.Dir(dir) + ".jsonl", nil
		}
	}
	if isLegacyAgentFile(path) {
		parentID := sessionIDFromFileContext(ctx, path)
		if err := ctx.Err(); err != nil {
			return "", err
		}
		candidates, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.jsonl"))
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if !isLegacyAgentFile(candidate) && sessionIDFromFileContext(ctx, candidate) == parentID {
				return candidate, nil
			}
		}
	}
	return path, nil
}

func isLegacyAgentFile(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, "agent-") && filepath.Ext(base) == ".jsonl"
}
