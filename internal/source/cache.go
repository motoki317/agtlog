package source

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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

const cacheVersion = 4

const maxSummaryCacheBytes = 64 << 20

const (
	cacheNamespaceBytes       = 16
	cacheTempDirName          = ".tmp"
	maxCacheTempSweepEntries  = 4096
	maxCacheTempSweepRemovals = 128
	staleCacheTempAge         = 24 * time.Hour
)

type fingerprinter interface {
	Fingerprint(string) (string, error)
}

func (r *Registry) loadCached(adapter Source, path, fingerprint string) (*model.Session, []DiscoveryDiagnostic, bool) {
	root, ok := r.openCacheRoot()
	if !ok {
		return nil, nil, false
	}
	defer func() { _ = root.Close() }()
	namespace, ok := cacheNamespace(adapter)
	if !ok {
		return nil, nil, false
	}
	info, err := root.Lstat(namespace)
	if err == nil {
		namespaceRoot, opened := openCacheDirectory(root, namespace, info)
		if !opened {
			return nil, nil, false
		}
		defer func() { _ = namespaceRoot.Close() }()
		session, diagnostics, valid, exists := loadCacheEntry(namespaceRoot, cacheEntryName(adapter, path), adapter, fingerprint)
		if valid {
			return session, diagnostics, true
		}
		if exists {
			return nil, nil, false
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, false
	}
	return nil, nil, false
}

func loadCacheEntry(root *os.Root, path string, adapter Source, fingerprint string) (*model.Session, []DiscoveryDiagnostic, bool, bool) {
	info, err := root.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() > maxSummaryCacheBytes {
		return nil, nil, false, !os.IsNotExist(err)
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return nil, nil, false, true
	}
	var entry cacheEntry
	if json.Unmarshal(data, &entry) != nil || entry.Version != cacheVersion || entry.Agent != adapter.Agent() || entry.Fingerprint != fingerprint || entry.Session == nil {
		return nil, nil, false, true
	}
	diagnostics := make([]DiscoveryDiagnostic, 0, len(entry.Diagnostics))
	for _, diagnostic := range entry.Diagnostics {
		diagnostics = append(diagnostics, DiscoveryDiagnostic{Agent: adapter.Agent(), Path: diagnostic.Path, Err: errors.New(diagnostic.Message)})
	}
	return entry.Session, diagnostics, true, true
}

func (r *Registry) storeCached(adapter Source, path, fingerprint string, session *model.Session, diagnostics []DiscoveryDiagnostic) {
	if r.options.CacheDir == "" || !r.cacheDirSafe() {
		return
	}
	namespace, ok := cacheNamespace(adapter)
	if !ok {
		return
	}
	cachedDiagnostics := make([]cacheDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		cachedDiagnostics = append(cachedDiagnostics, cacheDiagnostic{Path: diagnostic.Path, Message: diagnostic.Err.Error()})
	}
	data, err := json.Marshal(cacheEntry{Version: cacheVersion, Agent: adapter.Agent(), Fingerprint: fingerprint, Session: session, Diagnostics: cachedDiagnostics})
	if err != nil || len(data) > maxSummaryCacheBytes {
		return
	}
	root, ok := r.openOrCreateCacheRoot()
	if !ok {
		return
	}
	defer func() { _ = root.Close() }()
	if !ensureCacheDirectory(root, namespace) {
		return
	}
	namespaceInfo, err := root.Lstat(namespace)
	if err != nil {
		return
	}
	namespaceRoot, ok := openCacheDirectory(root, namespace, namespaceInfo)
	if !ok {
		return
	}
	defer func() { _ = namespaceRoot.Close() }()
	if !ensureCacheDirectory(namespaceRoot, cacheTempDirName) {
		return
	}
	temporary, temporaryPath, err := createCacheTemp(namespaceRoot)
	if err != nil {
		return
	}
	defer func() { _ = namespaceRoot.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return
	}
	if err := temporary.Close(); err != nil {
		return
	}
	_ = namespaceRoot.Rename(temporaryPath, cacheEntryName(adapter, path))
}

func ensureCacheDirectory(root *os.Root, path string) bool {
	if err := root.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return false
	}
	info, err := root.Lstat(path)
	return err == nil && cacheDirectorySafe(info)
}

func cacheDirectorySafe(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink == 0 && info.IsDir() && info.Mode().Perm()&0o022 == 0
}

func openCacheDirectory(root *os.Root, path string, expected os.FileInfo) (*os.Root, bool) {
	if !cacheDirectorySafe(expected) {
		return nil, false
	}
	opened, err := root.OpenRoot(path)
	if err != nil {
		return nil, false
	}
	actual, err := opened.Stat(".")
	if err != nil || !os.SameFile(expected, actual) {
		_ = opened.Close()
		return nil, false
	}
	return opened, true
}

func createCacheTemp(root *os.Root) (*os.File, string, error) {
	for range 100 {
		path := filepath.Join(cacheTempDirName, "summary-"+rand.Text()+".tmp")
		file, err := root.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		return file, path, err
	}
	return nil, "", errors.New("could not allocate cache temporary file")
}

func (r *Registry) openCacheRoot() (*os.Root, bool) {
	if r.options.CacheDir == "" || !r.cacheDirSafe() {
		return nil, false
	}
	root, err := os.OpenRoot(r.options.CacheDir)
	if err != nil {
		return nil, false
	}
	if !r.cacheDirSafe() {
		_ = root.Close()
		return nil, false
	}
	openedInfo, openedErr := root.Stat(".")
	pathInfo, pathErr := os.Stat(r.options.CacheDir)
	if openedErr != nil || pathErr != nil || !os.SameFile(openedInfo, pathInfo) {
		_ = root.Close()
		return nil, false
	}
	return root, true
}

func (r *Registry) openOrCreateCacheRoot() (*os.Root, bool) {
	if r.options.CacheDir == "" || !r.cacheDirSafe() {
		return nil, false
	}
	current := r.options.CacheDir
	var missing []string
	var currentInfo os.FileInfo
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, false
			}
			currentInfo = info
			break
		}
		if !os.IsNotExist(err) {
			return nil, false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, false
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	root, err := os.OpenRoot(current)
	if err != nil {
		return nil, false
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(currentInfo, openedInfo) {
		_ = root.Close()
		return nil, false
	}
	for index := len(missing) - 1; index >= 0; index-- {
		name := missing[index]
		if err := root.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
			_ = root.Close()
			return nil, false
		}
		info, err := root.Lstat(name)
		if err != nil {
			_ = root.Close()
			return nil, false
		}
		next, ok := openCacheDirectory(root, name, info)
		_ = root.Close()
		if !ok {
			return nil, false
		}
		root = next
	}
	if !r.cacheDirSafe() {
		_ = root.Close()
		return nil, false
	}
	openedInfo, openedErr := root.Stat(".")
	pathInfo, pathErr := os.Stat(r.options.CacheDir)
	if openedErr != nil || pathErr != nil || !os.SameFile(openedInfo, pathInfo) {
		_ = root.Close()
		return nil, false
	}
	return root, true
}

func (r *Registry) sweepStaleCacheTemps(now time.Time) {
	root, ok := r.openCacheRoot()
	if !ok {
		return
	}
	defer func() { _ = root.Close() }()
	remainingEntries := maxCacheTempSweepEntries
	remainingRemovals := maxCacheTempSweepRemovals
	seenNamespaces := make(map[string]bool)
	for _, adapter := range r.sources {
		namespace, namespaceOK := cacheNamespace(adapter)
		if !namespaceOK || seenNamespaces[namespace] {
			continue
		}
		seenNamespaces[namespace] = true
		info, err := root.Lstat(namespace)
		if err != nil {
			continue
		}
		namespaceRoot, opened := openCacheDirectory(root, namespace, info)
		if !opened {
			continue
		}
		sweepCacheTempDirectory(namespaceRoot, cacheTempDirName, now, &remainingEntries, &remainingRemovals)
		_ = namespaceRoot.Close()
	}
	for _, namespace := range sweepCacheRoot(root, now, &remainingEntries, &remainingRemovals) {
		if seenNamespaces[namespace] {
			continue
		}
		info, err := root.Lstat(namespace)
		if err != nil {
			continue
		}
		namespaceRoot, opened := openCacheDirectory(root, namespace, info)
		if !opened {
			continue
		}
		sweepCacheTempDirectory(namespaceRoot, cacheTempDirName, now, &remainingEntries, &remainingRemovals)
		_ = namespaceRoot.Close()
	}
}

func sweepCacheRoot(root *os.Root, now time.Time, remainingEntries, remainingRemovals *int) []string {
	if *remainingEntries == 0 || *remainingRemovals == 0 {
		return nil
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil
	}
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(*remainingEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	*remainingEntries -= len(names)
	var namespaces []string
	for _, name := range names {
		if isCacheNamespaceName(name) {
			namespaces = append(namespaces, name)
			continue
		}
		if *remainingRemovals == 0 || !strings.HasPrefix(name, "summary-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		info, statErr := root.Lstat(name)
		if statErr != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < staleCacheTempAge {
			continue
		}
		if root.Remove(name) == nil {
			*remainingRemovals--
		}
	}
	return namespaces
}

func sweepCacheTempDirectory(root *os.Root, directoryPath string, now time.Time, remainingEntries, remainingRemovals *int) {
	if *remainingEntries == 0 || *remainingRemovals == 0 {
		return
	}
	if directoryPath != "." {
		info, err := root.Lstat(directoryPath)
		if err != nil || !cacheDirectorySafe(info) {
			return
		}
	}
	directory, err := root.Open(directoryPath)
	if err != nil {
		return
	}
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(*remainingEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	*remainingEntries -= len(names)
	for _, name := range names {
		if *remainingRemovals == 0 {
			return
		}
		if !strings.HasPrefix(name, "summary-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		path := name
		if directoryPath != "." {
			path = filepath.Join(directoryPath, name)
		}
		info, statErr := root.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < staleCacheTempAge {
			continue
		}
		if root.Remove(path) == nil {
			*remainingRemovals--
		}
	}
}

func (r *Registry) removeCached(adapter Source, path string) {
	root, ok := r.openCacheRoot()
	if !ok {
		return
	}
	defer func() { _ = root.Close() }()
	namespace, ok := cacheNamespace(adapter)
	if !ok {
		return
	}
	info, err := root.Lstat(namespace)
	if err != nil {
		return
	}
	namespaceRoot, ok := openCacheDirectory(root, namespace, info)
	if !ok {
		return
	}
	defer func() { _ = namespaceRoot.Close() }()
	_ = namespaceRoot.Remove(cacheEntryName(adapter, path))
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
	namespaceDir, ok := r.cacheNamespaceDir(adapter)
	if !ok {
		return ""
	}
	return filepath.Join(namespaceDir, cacheEntryName(adapter, path))
}

func (r *Registry) cacheNamespaceDir(adapter Source) (string, bool) {
	namespace, ok := cacheNamespace(adapter)
	return filepath.Join(r.options.CacheDir, namespace), ok
}

func cacheNamespace(adapter Source) (string, bool) {
	fingerprint := adapter.CacheFingerprint()
	if fingerprint == "" {
		return "", false
	}
	hash := sha256.Sum256([]byte(fingerprint))
	return hex.EncodeToString(hash[:cacheNamespaceBytes]), true
}

func isCacheNamespaceName(name string) bool {
	if len(name) != 2*cacheNamespaceBytes {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

func cacheEntryName(adapter Source, path string) string {
	hash := sha256.Sum256([]byte(string(adapter.Agent()) + "\x00" + path))
	return hex.EncodeToString(hash[:]) + ".json"
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
