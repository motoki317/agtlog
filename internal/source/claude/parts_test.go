package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

type memoryParts struct {
	data   map[string][]byte
	stores map[string]int
}

func newMemoryParts() *memoryParts {
	return &memoryParts{data: make(map[string][]byte), stores: make(map[string]int)}
}

func (m *memoryParts) load(path string) ([]byte, bool) {
	data, ok := m.data[path]
	return data, ok
}

func (m *memoryParts) store(path string, data []byte) {
	m.data[path] = data
	m.stores[path]++
}

func appendPartRecord(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("\n" + `{"type":"assistant","timestamp":"2026-06-07T08:09:10Z","requestId":"cache-request","message":{"id":"cache-message","model":"claude-opus-4-8","usage":{"input_tokens":7,"output_tokens":3},"content":[{"type":"text","text":"Cache verification"}]}}` + "\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append record: write = %v, close = %v", writeErr, closeErr)
	}
}

func assertPartsParity(t *testing.T, parser Parser, path string, parts *memoryParts) *model.Session {
	t.Helper()
	want, err := parser.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parser.ParseWithPartsContext(context.Background(), path, nil, parts.load, parts.store)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("part parse differs from full parse:\ngot %s\nwant %s", gotJSON, wantJSON)
	}
	return got
}

func TestPartCacheFixtureParity(t *testing.T) {
	err := filepath.WalkDir("testdata", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return err
		}
		relative, err := filepath.Rel("testdata", path)
		if err != nil {
			return err
		}
		t.Run(relative, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS("testdata")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, relative)
			childPath := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "agent-cache-child.jsonl")
			if err := os.MkdirAll(filepath.Dir(childPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(childPath, []byte(`{"type":"user","agentId":"cache-child","message":{"content":"Check the cache"}}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			parser, parts := testParser(), newMemoryParts()
			assertPartsParity(t, parser, path, parts)
			assertPartsParity(t, parser, path, parts)
			for file, count := range parts.stores {
				if count != 1 {
					t.Fatalf("warm parse stored %s %d times, want once", file, count)
				}
			}
			appendPartRecord(t, childPath)
			assertPartsParity(t, parser, path, parts)
			appendPartRecord(t, path)
			assertPartsParity(t, parser, path, parts)
			for file, count := range parts.stores {
				want := 1
				if file == path || file == childPath {
					want = 2
				}
				if count != want {
					t.Errorf("stores for %s = %d, want %d", file, count, want)
				}
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPartCacheLegacyParity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-main.jsonl")
	legacyPath := filepath.Join(dir, "agent-legacy.jsonl")
	unrelatedPath := filepath.Join(dir, "agent-unrelated.jsonl")
	for file, line := range map[string]string{
		path:          `{"type":"user","sessionId":"session-main","message":{"content":"Coordinate the survey"}}`,
		legacyPath:    `{"type":"user","sessionId":"session-main","agentId":"legacy","message":{"content":"Inspect the river"}}`,
		unrelatedPath: `{"type":"user","sessionId":"session-other","agentId":"unrelated","message":{"content":"Inspect the mountain"}}`,
	} {
		if err := os.WriteFile(file, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parser, parts := testParser(), newMemoryParts()
	assertPartsParity(t, parser, path, parts)
	session := assertPartsParity(t, parser, path, parts)
	if len(session.Subagents) != 1 || session.Subagents[0].ID != "legacy" || len(parts.data) != 2 {
		t.Fatalf("legacy session = %#v, parts = %d", session, len(parts.data))
	}
	appendPartRecord(t, legacyPath)
	assertPartsParity(t, parser, path, parts)
	appendPartRecord(t, path)
	assertPartsParity(t, parser, path, parts)
	if err := os.WriteFile(legacyPath, []byte(`{"type":"user","sessionId":"session-other","agentId":"legacy"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if session := assertPartsParity(t, parser, path, parts); len(session.Subagents) != 0 {
		t.Fatal("legacy session ID change retained the child")
	}
}

func TestPartCacheRereadsAssemblyInputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "flat-sidecars"))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "subagents", "session-orchard.jsonl")
	childDir := filepath.Join(dir, "subagents", "session-orchard", "subagents")
	parser, parts := testParser(), newMemoryParts()
	assertPartsParity(t, parser, path, parts)
	if err := os.WriteFile(filepath.Join(childDir, "agent-reviewer.meta.json"), []byte(`{"parentAgentId":"east","toolUseId":"new-spawn"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPartsParity(t, parser, path, parts)
	newChild := filepath.Join(childDir, "agent-late.jsonl")
	if err := os.WriteFile(newChild, []byte(`{"type":"user","timestamp":"2026-12-30T12:00:00Z","agentId":"late","message":{"content":"Review late changes"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPartsParity(t, parser, path, parts)
	if err := os.Remove(newChild); err != nil {
		t.Fatal(err)
	}
	assertPartsParity(t, parser, path, parts)
	for file, count := range parts.stores {
		if count != 1 {
			t.Errorf("assembly change reparsed %s: stores = %d", file, count)
		}
	}
}

func TestPartCacheReusesOnlyUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "project-alpha"))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-main.jsonl")
	childPath := filepath.Join(dir, "session-main", "subagents", "agent-scout.jsonl")
	parser, parts := testParser(), newMemoryParts()
	cache := partCache{load: parts.load, store: parts.store}
	calls := make(map[string]int)
	parse := func(ctx context.Context, path string) (*model.Session, map[string]string, string, error) {
		calls[path]++
		return parser.parseFile(ctx, path)
	}
	for range 2 {
		for _, file := range []string{path, childPath} {
			if _, _, _, err := cache.parseFile(context.Background(), file, parse); err != nil {
				t.Fatal(err)
			}
		}
	}
	appendPartRecord(t, path)
	for _, file := range []string{path, childPath} {
		if _, _, _, err := cache.parseFile(context.Background(), file, parse); err != nil {
			t.Fatal(err)
		}
	}
	if calls[path] != 2 || calls[childPath] != 1 {
		t.Fatalf("parse calls = %v, want main twice and child once", calls)
	}
	info, err := os.Stat(childPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(childPath, info.ModTime(), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := cache.parseFile(context.Background(), childPath, parse); err != nil || calls[childPath] != 2 {
		t.Fatalf("mtime-only change: calls = %v, error = %v", calls, err)
	}
}

func TestPartCacheRejectsInvalidPayload(t *testing.T) {
	for _, shape := range []string{"path", "mtime", "size", "session path", "agent", "missing session", "subagents", "invalid JSON", "trailing JSON"} {
		t.Run(shape, func(t *testing.T) {
			parser, parts := testParser(), newMemoryParts()
			path := mainFixture()
			assertPartsParity(t, parser, path, parts)
			var part filePart
			if err := json.Unmarshal(parts.data[path], &part); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case "path":
				part.Fingerprint.Path += ".other"
			case "mtime":
				part.Fingerprint.ModTime++
			case "size":
				part.Fingerprint.Size++
			case "session path":
				part.Session.Path += ".other"
			case "agent":
				part.Session.Agent = model.AgentCodex
			case "missing session":
				part.Session = nil
			case "subagents":
				part.Session.Subagents = []*model.Session{{ID: "unexpected-child"}}
			}
			data, err := json.Marshal(part)
			if err != nil {
				t.Fatal(err)
			}
			if shape == "invalid JSON" {
				data = []byte(`{`)
			} else if shape == "trailing JSON" {
				data = append(data, []byte(`{}`)...)
			}
			parts.data[path] = data
			assertPartsParity(t, parser, path, parts)
			if parts.stores[path] != 2 {
				t.Fatalf("invalid part was reused: stores = %d", parts.stores[path])
			}
		})
	}
}

func TestPartCacheDoesNotStoreUnstableOrFailedParse(t *testing.T) {
	for _, scenario := range []string{"append", "removed", "parse error", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"Check stability"}}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			parser, parts := testParser(), newMemoryParts()
			cache := partCache{load: parts.load, store: parts.store}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			parse := func(ctx context.Context, path string) (*model.Session, map[string]string, string, error) {
				session, names, title, err := parser.parseFile(ctx, path)
				switch scenario {
				case "append":
					appendPartRecord(t, path)
				case "removed":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "parse error":
					err = errors.New("fixture parse error")
				case "canceled":
					cancel()
				}
				return session, names, title, err
			}
			_, _, _, err := cache.parseFile(ctx, path, parse)
			if scenario == "parse error" && err == nil {
				t.Fatal("parse error was swallowed")
			}
			if len(parts.data) != 0 {
				t.Fatal("stored an unstable or failed part")
			}
		})
	}
}
