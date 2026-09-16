package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"repoplane/internal/store"
)

func TestAuditOperationIsResolvedOnce(t *testing.T) {
	database, err := OpenAudit(context.Background(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	event := store.AuditEvent{
		RequestID: "request", PrincipalHash: "principal", WorkspaceID: "workspace",
		Method: "tools/call", Tool: "repoplane_read", Decision: "admitted", Status: "started",
		StartedAt: time.Now().UTC(),
	}
	if err := database.AdmitAudit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := database.ResolveAuditOperation(context.Background(), event.RequestID, "workspace_search"); err != nil {
		t.Fatal(err)
	}
	var operation string
	if err := database.db.QueryRow(`SELECT operation FROM audit_events WHERE request_id=?`, event.RequestID).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	if operation != "workspace_search" {
		t.Fatalf("operation=%q", operation)
	}
	if err := database.ResolveAuditOperation(context.Background(), event.RequestID, "catalog_query"); err == nil {
		t.Fatal("audit operation changed after resolution")
	}
}
