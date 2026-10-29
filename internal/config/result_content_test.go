package config

import (
	"io"
	"testing"
)

func TestParseResultContentMode(t *testing.T) {
	defaults, err := Parse(nil, io.Discard)
	if err != nil || defaults.ResultContent != "compatible" {
		t.Fatalf("default mode=%q err=%v", defaults.ResultContent, err)
	}
	for _, mode := range []string{"compatible", "deduplicated"} {
		settings, err := Parse([]string{"--result-content", mode}, io.Discard)
		if err != nil || settings.ResultContent != mode {
			t.Fatalf("mode=%q settings=%+v err=%v", mode, settings, err)
		}
	}
	if _, err := Parse([]string{"--result-content", "automatic"}, io.Discard); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
