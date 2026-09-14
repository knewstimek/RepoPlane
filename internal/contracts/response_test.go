package contracts

import (
	"encoding/json"
	"testing"
)

func TestEmptyResponseSerializesArrays(t *testing.T) {
	zero := uint64(0)
	truncated := false
	response := Response[struct{}]{
		Status:    StatusOK,
		Items:     EmptyItems[struct{}](),
		Counts:    Counts{Matched: &zero, Relation: CountExact, Returned: 0},
		Scan:      Scan{State: ScanComplete},
		Truncated: &truncated,
		Warnings:  EmptyWarnings(),
	}

	got, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"status":"ok","items":[],"counts":{"matched":0,"relation":"exact","returned":0},"scan":{"state":"complete","scope_ref":null},"truncated":false,"next_cursor":null,"snapshot_ref":null,"warnings":[]}`
	if string(got) != want {
		t.Fatalf("unexpected JSON\n got: %s\nwant: %s", got, want)
	}
}
