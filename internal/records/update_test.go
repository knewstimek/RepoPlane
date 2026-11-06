package records

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"repoplane/internal/store"
)

func TestUpdatingSuccessorPreservesSupersessionLink(t *testing.T) {
	s, _ := testServiceWithGit(t, false)
	ctx := context.Background()
	old, err := s.WriteMemo(ctx, MemoRequest{Mode: "create", MemoKind: "decision", Content: "before"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.WriteMemo(ctx, MemoRequest{Mode: "create", MemoKind: "decision", Content: "successor", Supersedes: old.Record.ID, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.WriteMemo(ctx, MemoRequest{Mode: "update", ID: next.Record.ID, ExpectedRevision: 1, Content: "revised successor", ResponseView: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Record.Supersedes != old.Record.ID {
		t.Fatal("updating a successor erased its supersession link")
	}
}

func TestMemoUpdatePreservesOmittedFieldsAndAllowsExplicitClears(t *testing.T) {
	s, _ := testServiceWithGit(t, false)
	ctx := context.Background()
	created, err := s.WriteMemo(ctx, MemoRequest{Mode: "create", MemoKind: "decision", Scope: "build", Configuration: "test", TopicKey: "policy",
		Content: "before", Title: "Policy", Summary: "Existing guidance", InvalidationCondition: "new evidence", Source: "llm_proposed",
		TemporalKind: "historical_observation", AsOf: "2026-01-01T00:00:00Z", EvidenceRefs: []string{"record:evidence"}, ResponseView: "full"})
	if err != nil {
		t.Fatal(err)
	}
	var request MemoRequest
	wire, _ := json.Marshal(map[string]any{"mode": "update", "id": created.Record.ID, "expected_revision": 1, "content": "after", "response_view": "full"})
	if err := json.Unmarshal(wire, &request); err != nil {
		t.Fatal(err)
	}
	updated, err := s.WriteMemo(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Record.ID != created.Record.ID || updated.Record.Revision != 2 || updated.Record.Source != "llm_proposed" || len(updated.Record.EvidenceRefs) != 1 {
		t.Fatalf("metadata lost: %+v", updated)
	}
	for key, before := range created.Record.Payload {
		if key == "content" {
			continue
		}
		if updated.Record.Payload[key] != before {
			t.Fatalf("omitted %s lost: %v", key, updated.Record.Payload[key])
		}
	}
	if updated.Record.Payload["content"] != "after" {
		t.Fatal("content not updated")
	}
	if _, err := s.WriteMemo(ctx, request); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	wire, _ = json.Marshal(map[string]any{"mode": "update", "id": created.Record.ID, "expected_revision": 2, "title": "", "summary": "", "temporal_kind": "", "as_of": "", "evidence_refs": []string{}, "response_view": "full"})
	if err := json.Unmarshal(wire, &request); err != nil {
		t.Fatal(err)
	}
	cleared, err := s.WriteMemo(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Record.Payload["title"] != nil || cleared.Record.Payload["as_of"] != nil || len(cleared.Record.EvidenceRefs) != 0 || cleared.Record.Payload["topic_key"] != "policy" {
		t.Fatalf("explicit clear failed: %+v", cleared)
	}
	_, err = s.WriteMemo(ctx, MemoRequest{Mode: "update", ID: created.Record.ID, ExpectedRevision: 3, TopicKey: "renamed"})
	var transition *TransitionError
	if !errors.As(err, &transition) || transition.Field != "topic_key" {
		t.Fatalf("rename lacks actionable field: %v", err)
	}
	read, err := s.Query(ctx, QueryRequest{Mode: "get", ID: created.Record.ID})
	if err != nil || read.Items[0].Revision != 3 {
		t.Fatalf("rejected rename changed record: %+v %v", read, err)
	}
}

func TestCheckpointUpdatePreservesOmittedFields(t *testing.T) {
	s, _ := testServiceWithGit(t, false)
	ctx := context.Background()
	dirty := true
	created, err := s.WriteCheckpoint(ctx, CheckpointRequest{Mode: "create", Goal: "finish", Dirty: &dirty, NextAction: "review", ChangeSummary: "implemented", BackgroundRefs: []string{"record:memo"}, RunRefs: []string{"record:run"}, Risks: []string{"pending"}, EvidenceRefs: []string{"record:evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.WriteCheckpoint(ctx, CheckpointRequest{Mode: "update", ID: created.Record.ID, ExpectedRevision: 1, NextAction: "ship", ResponseView: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Record.Payload["goal"] != "finish" || updated.Record.Payload["dirty"] != true || updated.Record.Payload["change_summary"] != "implemented" || len(updated.Record.EvidenceRefs) != 1 || len(updated.Record.Payload["background_refs"].([]any)) != 1 {
		t.Fatalf("checkpoint fields erased: %+v", updated)
	}
	wire, _ := json.Marshal(map[string]any{"mode": "update", "id": created.Record.ID, "expected_revision": 2, "next_action": "", "dirty": false, "risks": []string{}, "response_view": "full"})
	var request CheckpointRequest
	if err := json.Unmarshal(wire, &request); err != nil {
		t.Fatal(err)
	}
	cleared, err := s.WriteCheckpoint(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Record.Payload["next_action"] != "" || cleared.Record.Payload["dirty"] != false || len(cleared.Record.Payload["risks"].([]any)) != 0 || cleared.Record.Payload["goal"] != "finish" {
		t.Fatalf("checkpoint clears failed: %+v", cleared)
	}
}
