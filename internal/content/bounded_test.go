package content

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadBounded(t *testing.T) {
	got, truncated, err := ReadBounded(strings.NewReader("abcdef"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc" || !truncated {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}

	got, truncated, err = ReadBounded(strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc" || truncated {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}
}

func TestHashBounded(t *testing.T) {
	got, size, err := HashBounded(context.Background(), strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want || size != 3 {
		t.Fatalf("hash=%q size=%d", got, size)
	}
}

func TestHashBoundedRejectsPartialIdentity(t *testing.T) {
	got, _, err := HashBounded(context.Background(), strings.NewReader("abcd"), 3)
	if !errors.Is(err, ErrTooLarge) || got != "" {
		t.Fatalf("hash=%q error=%v, want empty hash and ErrTooLarge", got, err)
	}
}

func TestHashBoundedHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := HashBounded(ctx, strings.NewReader("abc"), 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}
