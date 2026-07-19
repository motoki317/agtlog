package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/motoki317/agtlog/internal/model"
)

type cacheEntry struct {
	Fingerprint string         `json:"fingerprint"`
	Session     *model.Session `json:"session"`
}

type fingerprinter interface {
	Fingerprint(string) (string, error)
}

func (r *Registry) loadCached(adapter Source, path string) (*model.Session, bool) {
	if r.options.CacheDir == "" {
		return nil, false
	}
	fingerprint, err := sourceFingerprint(adapter, path)
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(r.cachePath(path))
	if err != nil {
		return nil, false
	}
	var entry cacheEntry
	if json.Unmarshal(data, &entry) != nil || entry.Fingerprint != fingerprint || entry.Session == nil {
		return nil, false
	}
	return entry.Session, true
}

func (r *Registry) storeCached(adapter Source, path string, session *model.Session) {
	if r.options.CacheDir == "" {
		return
	}
	fingerprint, err := sourceFingerprint(adapter, path)
	if err != nil {
		return
	}
	data, err := json.Marshal(cacheEntry{Fingerprint: fingerprint, Session: session})
	if err != nil || os.MkdirAll(r.options.CacheDir, 0o700) != nil {
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
	_ = os.Rename(temporaryPath, r.cachePath(path))
}

func sourceFingerprint(adapter Source, path string) (string, error) {
	if custom, ok := adapter.(fingerprinter); ok {
		return custom.Fingerprint(path)
	}
	return fileFingerprint(path)
}

func (r *Registry) cachePath(path string) string {
	hash := sha256.Sum256([]byte(path))
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
