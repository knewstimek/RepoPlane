package cursor

import (
	"errors"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	codec, err := NewCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	codec.now = func() time.Time { return now }
	encoded, err := codec.Encode("workspace", "results", 42, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(encoded, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if got.ResultSetID != "results" || got.NextOrdinal != 42 {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestRejectsTamperWorkspaceAndExpiry(t *testing.T) {
	codec, err := NewCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	codec.now = func() time.Time { return now }
	encoded, err := codec.Encode("workspace", "results", 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := codec.Decode(encoded+"x", "workspace"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered cursor error = %v, want ErrInvalid", err)
	}
	if _, err := codec.Decode(encoded, "other"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("workspace mismatch error = %v, want ErrInvalid", err)
	}

	expired, err := codec.Encode("workspace", "results", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Decode(expired, "workspace"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired cursor error = %v, want ErrExpired", err)
	}
}
