// Package cursor creates opaque, authenticated pagination cursors.
package cursor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalid = errors.New("cursor: invalid")
	ErrExpired = errors.New("cursor: expired")
)

const formatVersion = 1

type Payload struct {
	FormatVersion int    `json:"v"`
	WorkspaceID   string `json:"w"`
	ResultSetID   string `json:"r"`
	NextOrdinal   uint64 `json:"n"`
	ExpiresUnix   int64  `json:"e"`
}

type Codec struct {
	key []byte
	now func() time.Time
}

func NewCodec(key []byte) (*Codec, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("%w: key must contain at least 32 bytes", ErrInvalid)
	}
	return &Codec{key: append([]byte(nil), key...), now: time.Now}, nil
}

func (c *Codec) Encode(workspaceID, resultSetID string, nextOrdinal uint64, expiresAt time.Time) (string, error) {
	if workspaceID == "" || resultSetID == "" || expiresAt.IsZero() {
		return "", fmt.Errorf("%w: missing payload field", ErrInvalid)
	}
	payload, err := json.Marshal(Payload{
		FormatVersion: formatVersion,
		WorkspaceID:   workspaceID,
		ResultSetID:   resultSetID,
		NextOrdinal:   nextOrdinal,
		ExpiresUnix:   expiresAt.Unix(),
	})
	if err != nil {
		return "", err
	}
	signature := c.sign(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c *Codec) Decode(encoded, expectedWorkspaceID string) (Payload, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return Payload{}, ErrInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Payload{}, ErrInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, c.sign(payload)) {
		return Payload{}, ErrInvalid
	}
	var decoded Payload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Payload{}, ErrInvalid
	}
	if decoded.FormatVersion != formatVersion || decoded.WorkspaceID == "" || decoded.ResultSetID == "" {
		return Payload{}, ErrInvalid
	}
	if expectedWorkspaceID != "" && decoded.WorkspaceID != expectedWorkspaceID {
		return Payload{}, ErrInvalid
	}
	if !c.now().Before(time.Unix(decoded.ExpiresUnix, 0)) {
		return Payload{}, ErrExpired
	}
	return decoded, nil
}

func (c *Codec) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
