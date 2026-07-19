package source

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWatcherEmitsDebouncedAppend(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher([]string{root}, WatchOptions{Debounce: 20 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case change := <-watcher.Events():
		if !reflect.DeepEqual(change.Paths, []string{path}) {
			t.Fatalf("change paths = %v, want %v", change.Paths, []string{path})
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for append event")
	}
}

func TestWatcherRescanFindsMissedAppend(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher([]string{root}, WatchOptions{Debounce: 5 * time.Millisecond, RescanInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.watcher.Remove(root); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case change := <-watcher.Events():
		if !reflect.DeepEqual(change.Paths, []string{path}) {
			t.Fatalf("change paths = %v, want %v", change.Paths, []string{path})
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for rescan event")
	}
}

func TestFollowerReparsesChangedSession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: filepath.Join(root, "cache")})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case update := <-follower.Updates():
		if len(update.Sessions) != 1 || update.Sessions[0].ID != "cached-session" {
			t.Fatalf("update sessions = %#v", update.Sessions)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for parsed session update")
	}
}
