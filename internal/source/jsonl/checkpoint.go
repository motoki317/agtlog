package jsonl

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
)

const CheckpointHeadBytes = 4 << 10

type Checkpoint struct {
	Size          int64             `json:"size"`
	HeadLength    int64             `json:"headLength"`
	HeadHash      [sha256.Size]byte `json:"headHash"`
	ResumeAt      int64             `json:"resumeAt"`
	LastLineBytes int64             `json:"lastLineBytes"`
	LastLineHash  [sha256.Size]byte `json:"lastLineHash"`
}

func CheckpointValid(ctx context.Context, file *os.File, currentSize int64, checkpoint Checkpoint) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if checkpoint.Size < 0 || currentSize < checkpoint.Size || checkpoint.ResumeAt < 0 || checkpoint.ResumeAt > checkpoint.Size ||
		checkpoint.HeadLength != min(checkpoint.Size, int64(CheckpointHeadBytes)) {
		return false, nil
	}
	headHash, err := FileRangeHash(ctx, file, 0, checkpoint.HeadLength)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	if headHash != checkpoint.HeadHash {
		return false, nil
	}
	if checkpoint.ResumeAt == 0 {
		return checkpoint.LastLineBytes == 0, nil
	}
	if checkpoint.LastLineBytes <= 0 || checkpoint.LastLineBytes > checkpoint.ResumeAt {
		return false, nil
	}
	terminator := []byte{0}
	if _, err := file.ReadAt(terminator, checkpoint.ResumeAt-1); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	if terminator[0] != '\n' {
		return false, nil
	}
	boundaryHash, err := FileRangeHash(ctx, file, checkpoint.ResumeAt-checkpoint.LastLineBytes, checkpoint.LastLineBytes)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return boundaryHash == checkpoint.LastLineHash, nil
}

func CheckpointHead(ctx context.Context, file *os.File, size int64) (int64, [sha256.Size]byte, error) {
	length := min(size, int64(CheckpointHeadBytes))
	digest, err := FileRangeHash(ctx, file, 0, length)
	return length, digest, err
}

func FileRangeHash(ctx context.Context, file *os.File, offset, length int64) ([sha256.Size]byte, error) {
	if err := ctx.Err(); err != nil {
		return [sha256.Size]byte{}, err
	}
	if length < 0 {
		return [sha256.Size]byte{}, errors.New("negative hash length")
	}
	hasher := sha256.New()
	reader := io.NewSectionReader(file, offset, length)
	buffer := make([]byte, min(length, int64(64<<10)))
	for reader.Size() > 0 {
		if err := ctx.Err(); err != nil {
			return [sha256.Size]byte{}, err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			_, _ = hasher.Write(buffer[:read])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return [sha256.Size]byte{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func TerminatedLineHash(line []byte, metadata LineMetadata) (int64, [sha256.Size]byte) {
	terminatorLength := metadata.NextOffset - metadata.Offset - metadata.Length
	hasher := sha256.New()
	_, _ = hasher.Write(line)
	if terminatorLength == 2 {
		_, _ = hasher.Write([]byte{'\r'})
	}
	_, _ = hasher.Write([]byte{'\n'})
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return metadata.NextOffset - metadata.Offset, digest
}
