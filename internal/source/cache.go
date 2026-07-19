package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/motoki317/agtlog/internal/model"
)

type cacheEntry struct {
	Version     int             `json:"version"`
	Agent       model.AgentKind `json:"agent"`
	Fingerprint string          `json:"fingerprint"`
	Session     *model.Session  `json:"session"`
}

const cacheVersion = 2

const maxSummaryCacheBytes = 64 << 20

type fingerprinter interface {
	Fingerprint(string) (string, error)
}

func (r *Registry) loadCached(adapter Source, path, fingerprint string) (*model.Session, bool) {
	if !r.cacheDirSafe() {
		return nil, false
	}
	cachePath := r.cachePath(adapter, path)
	info, err := os.Lstat(cachePath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() > maxSummaryCacheBytes {
		return nil, false
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, false
	}
	var entry cacheEntry
	if json.Unmarshal(data, &entry) != nil || entry.Version != cacheVersion || entry.Agent != adapter.Agent() || entry.Fingerprint != fingerprint || entry.Session == nil {
		return nil, false
	}
	return entry.Session, true
}

func (r *Registry) storeCached(adapter Source, path, fingerprint string, session *model.Session) {
	if r.options.CacheDir == "" || !r.cacheDirSafe() {
		return
	}
	data, err := json.Marshal(cacheEntry{Version: cacheVersion, Agent: adapter.Agent(), Fingerprint: fingerprint, Session: session})
	if err != nil || os.MkdirAll(r.options.CacheDir, 0o700) != nil {
		return
	}
	if !r.cacheDirSafe() {
		return
	}
	temporary, err := os.CreateTemp(r.options.CacheDir, "summary-*.tmp")
	if err != nil {
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return
	}
	if err := temporary.Close(); err != nil {
		return
	}
	_ = os.Rename(temporaryPath, r.cachePath(adapter, path))
}

func (r *Registry) cacheDirSafe() bool {
	var roots []string
	for _, adapter := range r.sources {
		roots = append(roots, adapter.Roots()...)
	}
	return CacheDirOutsideRoots(r.options.CacheDir, roots)
}

func CacheDirOutsideRoots(cacheDir string, roots []string) bool {
	_, ok := ResolveCacheDir(cacheDir, roots)
	return ok
}

func ResolveCacheDir(cacheDir string, roots []string) (string, bool) {
	cachePath, err := resolveExistingPath(cacheDir)
	if err != nil {
		return "", false
	}
	for _, root := range roots {
		rootPath, resolveErr := resolveExistingPath(root)
		if resolveErr != nil {
			return "", false
		}
		if pathWithin(cachePath, rootPath) || pathWithin(rootPath, cachePath) {
			return "", false
		}
	}
	return cachePath, true
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func resolveExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	var missing []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(resolveErr) {
			return "", resolveErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", resolveErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func (r *Registry) discoverSession(adapter Source, path string) (*model.Session, error) {
	if r.options.CacheDir == "" {
		return adapter.Parse(path)
	}
	fingerprint, err := sourceFingerprint(adapter, path)
	if err == nil {
		if session, ok := r.loadCached(adapter, path, fingerprint); ok {
			return session, nil
		}
	}
	return r.parseAndCache(adapter, path, fingerprint, err == nil)
}

func (r *Registry) parseAndCache(adapter Source, path, before string, cacheable bool) (*model.Session, error) {
	session, err := adapter.Parse(path)
	if err != nil {
		return nil, err
	}
	if cacheable {
		after, fingerprintErr := sourceFingerprint(adapter, path)
		if fingerprintErr == nil && before == after {
			r.storeCached(adapter, path, after, session)
		}
	}
	return session, nil
}

func sourceFingerprint(adapter Source, path string) (string, error) {
	if custom, ok := adapter.(fingerprinter); ok {
		return custom.Fingerprint(path)
	}
	return fileFingerprint(path)
}

func (r *Registry) cachePath(adapter Source, path string) string {
	hash := sha256.Sum256([]byte(string(adapter.Agent()) + "\x00" + path))
	return filepath.Join(r.options.CacheDir, hex.EncodeToString(hash[:])+".json")
}

func fileFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("session path is a directory")
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()), nil
}
