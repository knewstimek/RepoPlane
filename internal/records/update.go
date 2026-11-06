package records

import (
	"bytes"
	"context"
	"encoding/json"

	"repoplane/internal/store"
)

// TransitionError contains only server-authored field names and guidance, never
// submitted values or storage diagnostics. It is safe to expose through MCP.
type TransitionError struct {
	Field  string
	Reason string
}

func (e *TransitionError) Error() string { return e.Field + " " + e.Reason }
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

// Remember JSON presence: omitted fields preserve values while explicit empty
// strings/arrays clear optional mutable values. Go callers use nonzero fields
// and non-nil slices; wire callers can explicitly supply zero values as well.
func (r *MemoRequest) UnmarshalJSON(data []byte) error {
	type plain MemoRequest
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(data, &present); err != nil {
		return err
	}
	*r = MemoRequest(value)
	r.presentFields = present
	return nil
}

func (r *CheckpointRequest) UnmarshalJSON(data []byte) error {
	type plain CheckpointRequest
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(data, &present); err != nil {
		return err
	}
	*r = CheckpointRequest(value)
	r.presentFields = present
	return nil
}

func (s *Service) recordForUpdate(ctx context.Context, kind, id string, revision uint64) (store.Record, error) {
	if id == "" || revision == 0 {
		return store.Record{}, invalidArgument(&ValidationError{Field: "id/expected_revision", Reason: "are required for update"})
	}
	current, err := s.records.GetRecord(ctx, s.projectID, s.workspaceID, id)
	if err != nil {
		return store.Record{}, mutationStoreError(err)
	}
	if current.Kind != kind {
		return store.Record{}, &TransitionError{Field: "id", Reason: "must identify a record of the tool's kind; use the matching write tool"}
	}
	if current.Revision != revision {
		return store.Record{}, store.ErrConflict
	}
	return current, nil
}

func mergeRecordUpdate(current store.Record, request any, present map[string]json.RawMessage, lists map[string][]string, merged any) error {
	var base, patch map[string]json.RawMessage
	if err := json.Unmarshal(current.Payload, &base); err != nil || base == nil {
		return ErrStorageFailure
	}
	data, err := json.Marshal(request)
	if err != nil {
		return invalidArgument(err)
	}
	if err := json.Unmarshal(data, &patch); err != nil {
		return invalidArgument(err)
	}
	// omitempty drops explicit empty values; restore them from the wire request.
	for key, value := range present {
		patch[key] = value
	}
	for key, list := range lists {
		if list != nil {
			patch[key], _ = json.Marshal(list)
		}
	}
	base["source"], _ = json.Marshal(current.Source)
	base["evidence_refs"], _ = json.Marshal(current.EvidenceRefs)
	for key, value := range patch {
		base[key] = value
	}
	data, err = json.Marshal(base)
	if err != nil {
		return ErrStorageFailure
	}
	if err := json.Unmarshal(data, merged); err != nil {
		return invalidArgument(err)
	}
	return nil
}
