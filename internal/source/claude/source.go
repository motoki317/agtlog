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
	return Source{parser: parser, roots: roots}
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
	return roots
}

func (s Source) Agent() model.AgentKind {
	return model.AgentClaude
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
			if entry.IsDir() && entry.Name() == "subagents" {
				return filepath.SkipDir
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

func (s Source) Fingerprint(path string) (string, error) {
	hash := sha256.New()
	paths := []string{path, strings.TrimSuffix(path, filepath.Ext(path))}
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
	return path
}
