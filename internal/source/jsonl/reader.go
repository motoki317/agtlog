package jsonl

import (
	"bufio"
	"context"
	"errors"
	"io"
)

const (
	readerBufferBytes = 64 * 1024
	MaxLineBytes      = 16 * 1024 * 1024
)

// LineMetadata locates one physical line. Length excludes the line ending, and
// NextOffset is the offset after the line ending. Terminated is false for a
// final line without a newline. An Oversized line exceeds MaxLineBytes, counting
// its line ending. It arrives empty with Length 0, so its physical size is
// NextOffset - Offset.
type LineMetadata struct {
	Offset     int64
	Length     int64
	NextOffset int64
	Terminated bool
	Oversized  bool
}

// ForEachContext skips empty and oversized lines and continues, so one
// oversized record does not fail the whole file.
func ForEachContext(ctx context.Context, reader io.Reader, visit func([]byte)) error {
	return ForEachContextWithOffset(ctx, reader, func(line []byte, _, _ int64) {
		visit(line)
	})
}

func ForEachContextWithOffset(ctx context.Context, reader io.Reader, visit func(line []byte, offset, length int64)) error {
	return ForEachContextWithMetadata(ctx, reader, func(line []byte, metadata LineMetadata) {
		if !metadata.Oversized && len(line) > 0 {
			visit(line, metadata.Offset, metadata.Length)
		}
	})
}

// ForEachContextWithMetadata visits every line, including empty and oversized
// lines, and holds at most MaxLineBytes of one line in memory.
func ForEachContextWithMetadata(ctx context.Context, reader io.Reader, visit func([]byte, LineMetadata)) error {
	buffered := bufio.NewReaderSize(reader, readerBufferBytes)
	line := make([]byte, 0, readerBufferBytes)
	tooLong := false
	var offset, position int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fragment, err := buffered.ReadSlice('\n')
		position += int64(len(fragment))
		if !tooLong {
			if len(line)+len(fragment) <= MaxLineBytes {
				line = append(line, fragment...)
			} else {
				tooLong = true
				line = line[:0]
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if position > offset {
			if !tooLong {
				line = trimLineEnding(line)
			}
			visit(line, LineMetadata{
				Offset:     offset,
				Length:     int64(len(line)),
				NextOffset: position,
				Terminated: err == nil,
				Oversized:  tooLong,
			})
		}
		line = line[:0]
		tooLong = false
		offset = position
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func trimLineEnding(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}
