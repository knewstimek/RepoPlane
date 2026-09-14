package contracts

import (
	"errors"
	"fmt"
	"time"
)

const (
	DefaultItemLimit   uint64 = 50
	MaximumItemLimit   uint64 = 500
	DefaultByteLimit   uint64 = 64 * 1024
	MaximumByteLimit   uint64 = 1024 * 1024
	DefaultTimeLimit          = 5 * time.Second
	MaximumTimeLimit          = 30 * time.Second
	MaximumJSONLRecord uint64 = 8 * 1024 * 1024
)

var ErrLimitExceeded = errors.New("limit exceeds configured maximum")

// Limits is the normalized bounded-work contract shared by search tools.
type Limits struct {
	ItemLimit uint64
	ByteLimit uint64
	TimeLimit time.Duration
}

// LimitRequest contains optional caller-provided limit values. Zero means use
// the documented default; negative durations are invalid.
type LimitRequest struct {
	ItemLimit   uint64 `json:"item_limit,omitempty"`
	ByteLimit   uint64 `json:"byte_limit,omitempty"`
	TimeLimitMS int64  `json:"time_limit_ms,omitempty"`
}

// NormalizeLimits applies defaults and rejects values above the public caps.
func NormalizeLimits(req LimitRequest) (Limits, error) {
	itemLimit := req.ItemLimit
	if itemLimit == 0 {
		itemLimit = DefaultItemLimit
	}
	byteLimit := req.ByteLimit
	if byteLimit == 0 {
		byteLimit = DefaultByteLimit
	}
	timeLimit := time.Duration(req.TimeLimitMS) * time.Millisecond
	if req.TimeLimitMS == 0 {
		timeLimit = DefaultTimeLimit
	}

	if req.TimeLimitMS < 0 {
		return Limits{}, fmt.Errorf("time_limit_ms: %w", ErrLimitExceeded)
	}
	if itemLimit > MaximumItemLimit {
		return Limits{}, fmt.Errorf("item_limit: %w", ErrLimitExceeded)
	}
	if byteLimit > MaximumByteLimit {
		return Limits{}, fmt.Errorf("byte_limit: %w", ErrLimitExceeded)
	}
	if timeLimit > MaximumTimeLimit {
		return Limits{}, fmt.Errorf("time_limit_ms: %w", ErrLimitExceeded)
	}

	return Limits{ItemLimit: itemLimit, ByteLimit: byteLimit, TimeLimit: timeLimit}, nil
}
