package jsonl

import (
	"bufio"
	"errors"
	"io"
)

const (
	readerBufferBytes = 64 * 1024
	maxLineBytes      = 16 * 1024 * 1024
)

// ForEach bounds memory per record and treats an oversized record like malformed JSON.
func ForEach(reader io.Reader, visit func([]byte)) error {
	buffered := bufio.NewReaderSize(reader, readerBufferBytes)
	line := make([]byte, 0, readerBufferBytes)
	tooLong := false
	for {
		fragment, err := buffered.ReadSlice('\n')
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
				visit(line)
			}
		}
		line = line[:0]
		tooLong = false
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
