// Package content provides bounded, cancellation-aware source operations.
package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var (
	ErrInvalidLimit = errors.New("content: limit must be greater than zero")
	ErrTooLarge     = errors.New("content: source exceeds limit")
)

// ReadBounded returns at most limit bytes and reports whether more source bytes
// existed. It reads at most one byte beyond the caller-visible limit.
func ReadBounded(r io.Reader, limit uint64) ([]byte, bool, error) {
	if limit == 0 {
		return nil, false, ErrInvalidLimit
	}
	limited := io.LimitReader(r, int64(limit)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if uint64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

// HashBounded calculates an identity only if the entire stream fits within
// maxBytes. It never returns a partial hash that could be mistaken for a source
// identity.
func HashBounded(ctx context.Context, r io.Reader, maxBytes uint64) (string, uint64, error) {
	if maxBytes == 0 {
		return "", 0, ErrInvalidLimit
	}
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	var total uint64
	for {
		if err := ctx.Err(); err != nil {
			return "", total, err
		}
		remaining := maxBytes - total
		readSize := uint64(len(buffer))
		if remaining < readSize {
			readSize = remaining + 1
		}
		n, err := r.Read(buffer[:readSize])
		if n > 0 {
			total += uint64(n)
			if total > maxBytes {
				return "", total, ErrTooLarge
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			return "sha256:" + hex.EncodeToString(hash.Sum(nil)), total, nil
		}
		if err != nil {
			return "", total, fmt.Errorf("read source: %w", err)
		}
		if n == 0 {
			return "", total, io.ErrNoProgress
		}
	}
}
