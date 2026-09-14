package contracts

import (
	"errors"
	"testing"
)

func TestNormalizeLimitsDefaults(t *testing.T) {
	got, err := NormalizeLimits(LimitRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ItemLimit != DefaultItemLimit || got.ByteLimit != DefaultByteLimit || got.TimeLimit != DefaultTimeLimit {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}

func TestNormalizeLimitsRejectsCaps(t *testing.T) {
	tests := []LimitRequest{
		{ItemLimit: MaximumItemLimit + 1},
		{ByteLimit: MaximumByteLimit + 1},
		{TimeLimitMS: MaximumTimeLimit.Milliseconds() + 1},
		{TimeLimitMS: -1},
	}
	for _, test := range tests {
		if _, err := NormalizeLimits(test); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("NormalizeLimits(%+v) error = %v, want ErrLimitExceeded", test, err)
		}
	}
}
