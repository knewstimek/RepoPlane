package usage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"repoplane/internal/store"
)

type testReader struct{}

func (testReader) QueryUsage(context.Context, string, string) ([]store.UsageAggregate, string, error) {
	return []store.UsageAggregate{
		{Transport: "stdio", Tool: "catalog_query", Outcome: "ok", Calls: 2, RequestBytes: 12, ResponseBytes: 30, DurationMS: 4},
		{Transport: "stdio", Tool: "run_execute", Outcome: "reused", Calls: 1, RequestBytes: 8, ResponseBytes: 9, DurationMS: 1},
	}, "2026-09-01", nil
}

func TestReportCountsObservedUsageWithoutSavingsEstimate(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	report, err := Query(context.Background(), testReader{}, "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Since != "2026-08-18" || report.Until != "2026-09-16" || report.Totals.Calls != 3 || report.Totals.ReusedRuns != 1 || report.Totals.ResponseBytes != 39 {
		t.Fatalf("report=%+v", report)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "saved") || strings.Contains(string(encoded), "token_count") {
		t.Fatalf("report claims unobserved savings: %s", encoded)
	}
	if _, err := Query(context.Background(), testReader{}, "2026-09-17", "2026-09-16", now); err == nil {
		t.Fatal("reversed range accepted")
	}
}
