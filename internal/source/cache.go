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
	Version     int               `json:"version"`
	Agent       model.AgentKind   `json:"agent"`
	Fingerprint string            `json:"fingerprint"`
	Session     *model.Session    `json:"session"`
	Diagnostics []cacheDiagnostic `json:"diagnostics"`
}

type cacheDiagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

const cacheVersion = 3

const maxSummaryCacheBytes = 64 << 20

type fingerprinter interface {
	Fingerprint(string) (string, error)
}

func (r *Registry) loadCached(adapter Source, path, fingerprint string) (*model.Session, []DiscoveryDiagnostic, bool) {
	if !r.cacheDirSafe() {
		return nil, nil, false
	}
	cachePath := r.cachePath(adapter, path)
	info, err := os.Lstat(cachePath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() > maxSummaryCacheBytes {
		return nil, nil, false
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, nil, false
	}
	var entry cacheEntry
	if json.Unmarshal(data, &entry) != nil || entry.Version != cacheVersion || entry.Agent != adapter.Agent() || entry.Fingerprint != fingerprint || entry.Session == nil {
		return nil, nil, false
	}
	diagnostics := make([]DiscoveryDiagnostic, 0, len(entry.Diagnostics))
	for _, diagnostic := range entry.Diagnostics {
		diagnostics = append(diagnostics, DiscoveryDiagnostic{Agent: adapter.Agent(), Path: diagnostic.Path, Err: errors.New(diagnostic.Message)})
	}
	return entry.Session, diagnostics, true
}

func (r *Registry) storeCached(adapter Source, path, fingerprint string, session *model.Session, diagnostics []DiscoveryDiagnostic) {
	if r.options.CacheDir == "" || !r.cacheDirSafe() {
		return
	}
	cachedDiagnostics := make([]cacheDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		cachedDiagnostics = append(cachedDiagnostics, cacheDiagnostic{Path: diagnostic.Path, Message: diagnostic.Err.Error()})
	}
	data, err := json.Marshal(cacheEntry{Version: cacheVersion, Agent: adapter.Agent(), Fingerprint: fingerprint, Session: session, Diagnostics: cachedDiagnostics})
	if err != nil || len(data) > maxSummaryCacheBytes || os.MkdirAll(r.options.CacheDir, 0o700) != nil {
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

func (r *Registry) discoverSession(adapter Source, path string) (*model.Session, []DiscoveryDiagnostic, error) {
	if r.options.CacheDir == "" {
		return parseSessionWithDiagnostics(adapter, path)
	}
	fingerprint, err := sourceFingerprint(adapter, path)
	if err == nil {
		if session, diagnostics, ok := r.loadCached(adapter, path, fingerprint); ok {
			return session, diagnostics, nil
		}
	}
	return r.parseAndCache(adapter, path, fingerprint, err == nil)
}

func (r *Registry) parseAndCache(adapter Source, path, before string, cacheable bool) (*model.Session, []DiscoveryDiagnostic, error) {
	session, diagnostics, err := parseSessionWithDiagnostics(adapter, path)
	if err != nil {
		return nil, diagnostics, err
	}
	if cacheable {
		after, fingerprintErr := sourceFingerprint(adapter, path)
		if fingerprintErr == nil && before == after {
			r.storeCached(adapter, path, after, session, diagnostics)
		}
	}
	return session, diagnostics, nil
}

type diagnosticParser interface {
	ParseWithDiagnostics(string, func(string, error)) (*model.Session, error)
}

func parseSessionWithDiagnostics(adapter Source, path string) (*model.Session, []DiscoveryDiagnostic, error) {
	parser, ok := adapter.(diagnosticParser)
	if !ok {
		session, err := adapter.Parse(path)
		return session, nil, err
	}
	var diagnostics []DiscoveryDiagnostic
	session, err := parser.ParseWithDiagnostics(path, func(diagnosticPath string, diagnosticErr error) {
		diagnostics = append(diagnostics, DiscoveryDiagnostic{Agent: adapter.Agent(), Path: diagnosticPath, Err: diagnosticErr})
	})
	return session, diagnostics, err
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
