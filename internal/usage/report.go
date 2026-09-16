// Package usage summarizes locally observed MCP tool calls without storing payloads.
package usage

import (
	"context"
	"errors"
	"time"

	"repoplane/internal/store"
)

const dateLayout = "2006-01-02"

type Totals struct {
	Calls         uint64 `json:"calls"`
	Errors        uint64 `json:"errors"`
	Approvals     uint64 `json:"approvals"`
	ReusedRuns    uint64 `json:"reused_runs"`
	RequestBytes  uint64 `json:"request_bytes"`
	ResponseBytes uint64 `json:"response_bytes"`
	DurationMS    uint64 `json:"duration_ms"`
}

type Report struct {
	SchemaVersion    string                 `json:"schema_version"`
	Since            string                 `json:"since"`
	Until            string                 `json:"until"`
	FirstObservedDay string                 `json:"first_observed_day,omitempty"`
	RetentionDays    int                    `json:"retention_days"`
	Measurement      string                 `json:"measurement"`
	Totals           Totals                 `json:"totals"`
	Rows             []store.UsageAggregate `json:"rows"`
}

type Reader interface {
	QueryUsage(context.Context, string, string) ([]store.UsageAggregate, string, error)
}

func Query(ctx context.Context, repository Reader, since, until string, now time.Time) (Report, error) {
	if until == "" {
		until = now.UTC().Format(dateLayout)
	}
	end, err := time.Parse(dateLayout, until)
	if err != nil {
		return Report{}, errors.New("until must be YYYY-MM-DD")
	}
	if since == "" {
		since = end.AddDate(0, 0, -29).Format(dateLayout)
	}
	start, err := time.Parse(dateLayout, since)
	if err != nil {
		return Report{}, errors.New("since must be YYYY-MM-DD")
	}
	if end.Before(start) || end.Sub(start) > 365*24*time.Hour {
		return Report{}, errors.New("usage range must span 1 to 366 days")
	}
	rows, first, err := repository.QueryUsage(ctx, since, until)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: "usage-report.v1", Since: since, Until: until,
		FirstObservedDay: first, RetentionDays: 365,
		Measurement: "observed tool calls; byte counts are serialized arguments/results before MCP framing; error response bytes are excluded; no token or future-savings estimate",
		Rows:        rows}
	for _, row := range rows {
		report.Totals.Calls += row.Calls
		report.Totals.RequestBytes += row.RequestBytes
		report.Totals.ResponseBytes += row.ResponseBytes
		report.Totals.DurationMS += row.DurationMS
		switch row.Outcome {
		case "error":
			report.Totals.Errors += row.Calls
		case "approval":
			report.Totals.Approvals += row.Calls
		case "reused":
			report.Totals.ReusedRuns += row.Calls
		}
	}
	return report, nil
}
