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
	maxLineBytes      = MaxLineBytes
)

// ForEach bounds memory per record and treats an oversized record like malformed JSON.
func ForEach(reader io.Reader, visit func([]byte)) error {
	return ForEachContext(context.Background(), reader, visit)
}

func ForEachContext(ctx context.Context, reader io.Reader, visit func([]byte)) error {
	return ForEachContextWithOffset(ctx, reader, func(line []byte, _, _ int64) {
		visit(line)
	})
}

func ForEachContextWithOffset(ctx context.Context, reader io.Reader, visit func([]byte, int64, int64)) error {
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
			if len(line)+len(fragment) <= maxLineBytes {
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
		if !tooLong {
			line = trimLineEnding(line)
			if len(line) > 0 {
				visit(line, offset, int64(len(line)))
			}
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
