package claude

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source/jsonl"
)

type filePart struct {
	Accumulator *fileAccumulator `json:"accumulator"`
	Checkpoint  jsonl.Checkpoint `json:"checkpoint"`
}

func (p Parser) parseFileWithParts(ctx context.Context, path string, parts partCache) (*model.Session, map[string]string, string, error) {
	if parts.load == nil || parts.store == nil {
		return p.parseFile(ctx, path)
	}
	var previous *filePart
	if data, ok := parts.load(path); ok {
		var part filePart
		if jsonl.Unmarshal(data, &part) == nil {
			previous = &part
		}
	}
	result, part, err := parseFilePart(ctx, path, previous, nil)
	if err != nil {
		return nil, nil, "", err
	}
	if parts.afterParse != nil {
		parts.afterParse(previous, part)
	}
	session, names, prompt, err := result.finish(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	if part != nil && (previous == nil || part.Accumulator != previous.Accumulator || previous.Checkpoint != part.Checkpoint) {
		data, err := json.Marshal(part)
		if err == nil && ctx.Err() == nil {
			parts.store(path, data)
		}
	}
	return session, names, prompt, ctx.Err()
}

func (part *filePart) valid(ctx context.Context, file *os.File, path string, size int64) (bool, error) {
	if part == nil || part.Accumulator == nil || part.Accumulator.Session.Path != path ||
		part.Accumulator.Session.Agent != model.AgentClaude || len(part.Accumulator.Session.Subagents) != 0 ||
		part.Accumulator.WorkflowNames == nil {
		return false, nil
	}
	return jsonl.CheckpointValid(ctx, file, size, part.Checkpoint)
}

func parseFilePart(ctx context.Context, path string, previous *filePart, afterValidation func()) (*fileAccumulator, *filePart, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	resumed, err := previous.valid(ctx, file, path, info.Size())
	if err != nil {
		return nil, nil, err
	}
	accumulator := newFileAccumulator(path)
	checkpoint := jsonl.Checkpoint{}
	if resumed {
		accumulator = previous.Accumulator
		checkpoint = previous.Checkpoint
		if afterValidation != nil {
			afterValidation()
		}
	}
	resumeAt := checkpoint.ResumeAt
	var rangeHash [sha256.Size]byte
	if resumed {
		rangeHash, err = jsonl.FileRangeHash(ctx, file, resumeAt, info.Size()-resumeAt)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return parseFilePart(ctx, path, nil, nil)
		}
	}
	var trailingLine []byte
	lastLineOffset := checkpoint.ResumeAt - checkpoint.LastLineBytes
	lastLineNeedsHash := false
	err = jsonl.ForEachContextWithMetadata(ctx, io.NewSectionReader(file, resumeAt, info.Size()-resumeAt), func(line []byte, metadata jsonl.LineMetadata) {
		if !metadata.Terminated {
			if !metadata.Oversized && len(line) > 0 {
				trailingLine = slices.Clone(line)
			}
			return
		}
		if !metadata.Oversized && len(line) > 0 {
			accumulator.ingest(line)
		}
		checkpoint.ResumeAt = resumeAt + metadata.NextOffset
		lastLineOffset = resumeAt + metadata.Offset
		checkpoint.LastLineBytes = metadata.NextOffset - metadata.Offset
		lastLineNeedsHash = metadata.Oversized
		if !metadata.Oversized {
			checkpoint.LastLineBytes, checkpoint.LastLineHash = jsonl.TerminatedLineHash(line, metadata)
		}
	})
	if err != nil {
		return nil, nil, err
	}
	if lastLineNeedsHash {
		checkpoint.LastLineHash, err = jsonl.FileRangeHash(ctx, file, lastLineOffset, checkpoint.LastLineBytes)
		if err != nil {
			return nil, nil, err
		}
	}
	var postScanHash [sha256.Size]byte
	var hashErr error
	if resumed {
		postScanHash, hashErr = jsonl.FileRangeHash(ctx, file, resumeAt, info.Size()-resumeAt)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}
	checkpoint.Size = info.Size()
	checkpoint.HeadLength, checkpoint.HeadHash, err = jsonl.CheckpointHead(ctx, file, info.Size())
	if err != nil {
		return nil, nil, err
	}
	postScanInfo, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if resumed {
		valid, validationErr := previous.valid(ctx, file, path, postScanInfo.Size())
		if validationErr != nil {
			return nil, nil, validationErr
		}
		if hashErr != nil || rangeHash != postScanHash || !valid {
			return parseFilePart(ctx, path, nil, nil)
		}
	}
	pathInfo, pathErr := os.Stat(path)
	if resumed && (pathErr != nil || !os.SameFile(info, pathInfo)) {
		return parseFilePart(ctx, path, nil, nil)
	}
	result := accumulator
	if len(trailingLine) > 0 {
		result = accumulator.clone()
		result.ingest(trailingLine)
	}
	if pathErr != nil || !os.SameFile(info, pathInfo) || info.Size() != postScanInfo.Size() ||
		info.ModTime() != postScanInfo.ModTime() || info.Size() != pathInfo.Size() || info.ModTime() != pathInfo.ModTime() {
		return result, nil, nil
	}
	return result, &filePart{Accumulator: accumulator, Checkpoint: checkpoint}, nil
}

func (a *fileAccumulator) clone() *fileAccumulator {
	cloned := *a
	cloned.WorkflowNames = maps.Clone(a.WorkflowNames)
	cloned.UsageRecords = slices.Clone(a.UsageRecords)
	return &cloned
}
