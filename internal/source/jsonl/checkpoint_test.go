package jsonl

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointValid(t *testing.T) {
	content := []byte("first\r\nlast\npartial" + strings.Repeat("x", CheckpointHeadBytes))
	path := filepath.Join(t.TempDir(), "records.jsonl")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	base := Checkpoint{
		Size: int64(len(content)), HeadLength: CheckpointHeadBytes, HeadHash: sha256.Sum256(content[:CheckpointHeadBytes]),
		ResumeAt: 12, LastLineBytes: 5, LastLineHash: sha256.Sum256(content[7:12]),
	}
	tests := []struct {
		name   string
		change func(*Checkpoint)
		size   int64
		want   bool
	}{
		{"unchanged", func(*Checkpoint) {}, base.Size, true},
		{"grew", func(*Checkpoint) {}, base.Size + 10, true},
		{"shrank", func(*Checkpoint) {}, base.Size - 1, false},
		{"head hash", func(c *Checkpoint) { c.HeadHash[0]++ }, base.Size, false},
		{"boundary hash", func(c *Checkpoint) { c.LastLineHash[0]++ }, base.Size, false},
		{"offset not after newline", func(c *Checkpoint) { c.ResumeAt-- }, base.Size, false},
		{"negative offset", func(c *Checkpoint) { c.ResumeAt = -1 }, base.Size, false},
		{"offset past file", func(c *Checkpoint) { c.ResumeAt = base.Size + 1 }, base.Size, false},
		{"offset past stored size", func(c *Checkpoint) {
			c.Size = 7
			c.HeadLength = 7
			c.HeadHash = sha256.Sum256(content[:7])
		}, base.Size, false},
		{"short head", func(c *Checkpoint) { c.HeadLength = 5; c.HeadHash = sha256.Sum256(content[:5]) }, base.Size, false},
		{"negative head length", func(c *Checkpoint) { c.HeadLength = -1 }, base.Size, false},
		{"head too long", func(c *Checkpoint) {
			c.HeadLength = CheckpointHeadBytes + 1
			c.HeadHash = sha256.Sum256(content[:CheckpointHeadBytes+1])
		}, base.Size, false},
		{"negative boundary length", func(c *Checkpoint) { c.LastLineBytes = -1 }, base.Size, false},
		{"boundary too long", func(c *Checkpoint) { c.LastLineBytes = c.ResumeAt + 1 }, base.Size, false},
		{"zero offset", func(c *Checkpoint) { c.ResumeAt = 0; c.LastLineBytes = 0 }, base.Size, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := base
			test.change(&c)
			got, err := CheckpointValid(context.Background(), file, test.size, c)
			if err != nil || got != test.want {
				t.Fatalf("CheckpointValid() = %v, %v, want %v", got, err, test.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckpointValid(ctx, file, base.Size, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation error = %v", err)
	}
}

func TestFileRangeHashRejectsNegativeLength(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "records-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := FileRangeHash(context.Background(), file, 0, -1); err == nil {
		t.Fatal("negative length returned no error")
	}
}

func TestCheckpointHead(t *testing.T) {
	content := []byte(strings.Repeat("x", CheckpointHeadBytes+1))
	path := filepath.Join(t.TempDir(), "records.jsonl")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, size := range []int64{0, 5, CheckpointHeadBytes, int64(len(content))} {
		length, digest, err := CheckpointHead(context.Background(), file, size)
		wantLength := min(size, int64(CheckpointHeadBytes))
		if err != nil || length != wantLength || digest != sha256.Sum256(content[:wantLength]) {
			t.Fatalf("CheckpointHead(%d) = %d, %x, %v", size, length, digest, err)
		}
	}
}

func TestTerminatedLineHash(t *testing.T) {
	for _, physical := range []string{"line\n", "line\r\n", "\n", "\r\n"} {
		line := []byte(physical[:len(physical)-1])
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		length, digest := TerminatedLineHash(line, LineMetadata{
			Offset: 10, Length: int64(len(line)), NextOffset: 10 + int64(len(physical)), Terminated: true,
		})
		if length != int64(len(physical)) || digest != sha256.Sum256([]byte(physical)) {
			t.Fatalf("TerminatedLineHash(%q) = %d, %x", physical, length, digest)
		}
	}
}
