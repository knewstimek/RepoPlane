package toolboxclient

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCacheInsertsHandleAndRequiresCompactionRehydration(t *testing.T) {
	cache := New()
	key := Key{ServerIdentity: "server", Surface: "toolbox.v1", Toolbox: "repoplane_read", Operation: "workspace_search"}
	handle := "schema:workspace_search@sha256:0123456789abcdef"
	contract := json.RawMessage(`{"schema_version":"operation-contract.v1"}`)
	if err := cache.Remember(key, handle, contract); err != nil {
		t.Fatal(err)
	}
	describe := cache.DescribeArguments(key)
	if describe["known_schema_handle"] != handle {
		t.Fatalf("describe=%v", describe)
	}
	call, err := cache.CallArguments(key, map[string]any{"mode": "filename"})
	if err != nil || call["schema_handle"] != handle {
		t.Fatalf("call=%v err=%v", call, err)
	}

	cache.Compact()
	describe = cache.DescribeArguments(key)
	if _, ok := describe["known_schema_handle"]; ok {
		t.Fatalf("non-resident describe reused handle: %v", describe)
	}
	if _, err := cache.CallArguments(key, map[string]any{}); !errors.Is(err, ErrRehydrateRequired) {
		t.Fatalf("compacted call err=%v", err)
	}
	rehydrated, err := cache.Rehydrate(key)
	if err != nil || string(rehydrated) != string(contract) {
		t.Fatalf("rehydrated=%s err=%v", rehydrated, err)
	}
	if _, err := cache.CallArguments(key, map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestCacheRejectsCrossOperationHandle(t *testing.T) {
	cache := New()
	key := Key{ServerIdentity: "server", Surface: "toolbox.v1", Toolbox: "repoplane_read", Operation: "workspace_search"}
	if err := cache.Remember(key, "schema:catalog_query@sha256:0123", json.RawMessage(`{}`)); !errors.Is(err, ErrContractInvalid) {
		t.Fatalf("cross-operation handle err=%v", err)
	}
}
