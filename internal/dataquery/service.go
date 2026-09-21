// Package dataquery provides bounded text ranges and streaming-style JSONL projection.
package dataquery

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"repoplane/internal/content"
	"repoplane/internal/contracts"
	"repoplane/internal/cursor"
	"repoplane/internal/store"
	"repoplane/internal/textcodec"
	"repoplane/internal/workspace"
)

const (
	MaxSourceBytes    uint64 = 64 * 1024 * 1024
	MaxProjectedBytes uint64 = 64 * 1024 * 1024
	MaxDataItems             = 100_000
	resultTTL                = 30 * time.Minute
)

var (
	ErrSourceChanged    = errors.New("data query: source_changed")
	ErrResponseTooLarge = errors.New("data query: response cannot fit byte_limit")
	ErrSourceTooLarge   = errors.New("data query: source exceeds supported size")
)

type Filter struct {
	Field     string `json:"field" jsonschema:"top-level field name"`
	ValueType string `json:"value_type" jsonschema:"string, boolean, null, or integer"`
	Value     string `json:"value,omitempty" jsonschema:"value text; integers remain decimal strings"`
}

type Request struct {
	Mode            string   `json:"mode,omitempty" jsonschema:"omit with cursor"`
	Ref             string   `json:"ref,omitempty" jsonschema:"source ref returned by another RepoPlane tool"`
	Encoding        string   `json:"encoding,omitempty" jsonschema:"utf-8, cp949, or euc-kr; defaults to utf-8"`
	LineStart       *uint64  `json:"line_start,omitempty" jsonschema:"one-based inclusive line start"`
	LineEnd         *uint64  `json:"line_end,omitempty" jsonschema:"one-based inclusive line end"`
	ByteStart       *uint64  `json:"byte_start,omitempty" jsonschema:"zero-based inclusive byte start"`
	ByteEnd         *uint64  `json:"byte_end,omitempty" jsonschema:"zero-based exclusive byte end"`
	Dialect         string   `json:"dialect,omitempty" jsonschema:"jsonl-simple, json-pointer, csv, tsv, exact, or regex"`
	Filters         []Filter `json:"filters,omitempty" jsonschema:"top-level equality filters"`
	Fields          []string `json:"fields,omitempty" jsonschema:"top-level fields to project; empty returns all fields"`
	MalformedPolicy string   `json:"malformed_policy,omitempty" jsonschema:"fail or skip_with_warning"`
	Pointer         string   `json:"pointer,omitempty" jsonschema:"RFC 6901 pointer for json mode; default root"`
	Pattern         string   `json:"pattern,omitempty" jsonschema:"exact text or RE2 pattern for log mode"`
	CaseSensitive   bool     `json:"case_sensitive,omitempty" jsonschema:"case-sensitive log matching"`
	Cursor          string   `json:"cursor,omitempty" jsonschema:"opaque cursor returned by an earlier data_query"`
	ItemLimit       uint64   `json:"item_limit,omitempty" jsonschema:"max returned items; default 50, max 500"`
	ByteLimit       uint64   `json:"byte_limit,omitempty" jsonschema:"max response bytes; default 65536, max 1048576"`
	TimeLimitMS     int64    `json:"time_limit_ms,omitempty" jsonschema:"deadline ms; default 5000, max 30000"`
}

type Result struct {
	Kind        string         `json:"kind"`
	SourceRef   string         `json:"source_ref"`
	Line        uint64         `json:"line,omitempty"`
	ByteStart   uint64         `json:"byte_start,omitempty"`
	ByteEnd     uint64         `json:"byte_end,omitempty"`
	Text        string         `json:"text,omitempty"`
	BytesBase64 string         `json:"bytes_base64,omitempty"`
	Record      map[string]any `json:"record,omitempty"`
	RecordIndex uint64         `json:"record_index,omitempty"`
}

type Metadata struct {
	Mode               string `json:"mode"`
	Dialect            string `json:"dialect,omitempty"`
	Encoding           string `json:"encoding"`
	MissingFieldPolicy string `json:"missing_field_policy,omitempty"`
}

type Response struct {
	Status      contracts.Status    `json:"status"`
	Items       []Result            `json:"items"`
	Counts      contracts.Counts    `json:"counts"`
	Scan        contracts.Scan      `json:"scan"`
	Truncated   *bool               `json:"truncated"`
	NextCursor  *string             `json:"next_cursor"`
	SnapshotRef *string             `json:"snapshot_ref"`
	Warnings    []contracts.Warning `json:"warnings"`
	Metadata    Metadata            `json:"metadata"`
}

type persistedMetadata struct {
	Status         contracts.Status        `json:"status"`
	Relation       contracts.CountRelation `json:"relation"`
	Scan           contracts.Scan          `json:"scan"`
	Warnings       []contracts.Warning     `json:"warnings"`
	Metadata       Metadata                `json:"metadata"`
	SourceIdentity string                  `json:"source_identity"`
}

type Service struct {
	root       *workspace.Root
	repository store.Repository
	cursors    *cursor.Codec
	now        func() time.Time
}

func NewService(root *workspace.Root, repository store.Repository, cursors *cursor.Codec) *Service {
	return &Service{root: root, repository: repository, cursors: cursors, now: time.Now}
}

func (s *Service) Query(ctx context.Context, request Request) (Response, error) {
	limits, err := contracts.NormalizeLimits(contracts.LimitRequest{
		ItemLimit: request.ItemLimit, ByteLimit: request.ByteLimit, TimeLimitMS: request.TimeLimitMS,
	})
	if err != nil {
		return Response{}, err
	}
	if request.Cursor != "" {
		return s.fromCursor(ctx, request.Cursor, limits.ItemLimit, limits.ByteLimit)
	}
	if request.Ref == "" {
		return Response{}, errors.New("ref is required")
	}
	if request.Encoding == "" {
		request.Encoding = "utf-8"
	}
	encoding, err := textcodec.Normalize(request.Encoding)
	if err != nil {
		return unsupportedResponse(request, err), nil
	}
	request.Encoding = encoding
	data, err := s.readSource(request.Ref)
	if err != nil {
		return Response{}, err
	}
	scanCtx, cancel := context.WithTimeout(ctx, limits.TimeLimit)
	defer cancel()

	switch request.Mode {
	case "text_range":
		results, metadata, err := textRange(scanCtx, request, data)
		if err != nil {
			return Response{}, err
		}
		return s.persist(ctx, request, data, results, completeMetadata(metadata), limits.ItemLimit, limits.ByteLimit)
	case "jsonl":
		if request.Dialect == "" {
			request.Dialect = "jsonl-simple"
		}
		if request.MalformedPolicy == "" {
			request.MalformedPolicy = "fail"
		}
		results, metadata, status, relation, scan, warnings, err := queryJSONL(scanCtx, request, data)
		if err != nil {
			return Response{}, err
		}
		return s.persist(ctx, request, data, results, persistedMetadata{
			Status: status, Relation: relation, Scan: scan, Warnings: warnings, Metadata: metadata,
		}, limits.ItemLimit, limits.ByteLimit)
	case "json":
		results, metadata, err := queryJSON(scanCtx, request, data)
		if err != nil {
			return Response{}, err
		}
		return s.persist(ctx, request, data, results, completeMetadata(metadata), limits.ItemLimit, limits.ByteLimit)
	case "delimited":
		results, metadata, status, relation, scan, warnings, err := queryDelimited(scanCtx, request, data)
		if err != nil {
			return Response{}, err
		}
		return s.persist(ctx, request, data, results, persistedMetadata{Status: status, Relation: relation, Scan: scan, Warnings: warnings, Metadata: metadata}, limits.ItemLimit, limits.ByteLimit)
	case "log":
		results, metadata, status, relation, scan, warnings, err := queryLog(scanCtx, request, data)
		if err != nil {
			return Response{}, err
		}
		return s.persist(ctx, request, data, results, persistedMetadata{Status: status, Relation: relation, Scan: scan, Warnings: warnings, Metadata: metadata}, limits.ItemLimit, limits.ByteLimit)
	default:
		return Response{}, fmt.Errorf("unsupported data query mode %q", request.Mode)
	}
}

func queryJSON(ctx context.Context, request Request, data []byte) ([]Result, Metadata, error) {
	if request.Dialect == "" {
		request.Dialect = "json-pointer"
	}
	metadata := Metadata{Mode: "json", Dialect: request.Dialect, Encoding: request.Encoding, MissingFieldPolicy: "missing filter fields do not match; projected missing fields are null"}
	if request.Dialect != "json-pointer" {
		return nil, metadata, fmt.Errorf("unsupported JSON dialect %q", request.Dialect)
	}
	for _, filter := range request.Filters {
		if err := validateFilter(filter); err != nil {
			return nil, metadata, err
		}
	}
	text, err := textcodec.Decode(request.Encoding, data)
	if err != nil {
		return nil, metadata, err
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, metadata, fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, metadata, errors.New("JSON contains trailing value or data")
	}
	value, err = resolveJSONPointer(value, request.Pointer)
	if err != nil {
		return nil, metadata, err
	}
	values := []any{value}
	if array, ok := value.([]any); ok {
		values = array
	}
	results := make([]Result, 0, len(values))
	var projectedBytes uint64
	for index, item := range values {
		if err := ctx.Err(); err != nil {
			return nil, metadata, err
		}
		record, ok := item.(map[string]any)
		if !ok {
			record = map[string]any{"value": item}
		}
		if !matchesFilters(record, request.Filters) {
			continue
		}
		projected := project(record, request.Fields)
		encoded, err := json.Marshal(projected)
		if err != nil {
			return nil, metadata, err
		}
		projectedBytes += uint64(len(encoded))
		if len(results) >= MaxDataItems || projectedBytes > MaxProjectedBytes {
			return nil, metadata, errors.New("JSON projection exceeds supported limit")
		}
		results = append(results, Result{Kind: "json_value", SourceRef: request.Ref, RecordIndex: uint64(index + 1), Record: projected})
	}
	return results, metadata, nil
}

func resolveJSONPointer(value any, pointer string) (any, error) {
	if pointer == "" {
		return value, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("JSON pointer must be empty or begin with /")
	}
	current := value
	for _, raw := range strings.Split(pointer[1:], "/") {
		token, err := decodePointerToken(raw)
		if err != nil {
			return nil, err
		}
		switch typed := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = typed[token]
			if !ok {
				return nil, fmt.Errorf("JSON pointer member %q is missing", token)
			}
		case []any:
			index, err := strconv.ParseUint(token, 10, 64)
			if err != nil || index >= uint64(len(typed)) {
				return nil, fmt.Errorf("JSON pointer array index %q is invalid", token)
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("JSON pointer cannot descend through %q", token)
		}
	}
	return current, nil
}

func decodePointerToken(raw string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(raw); index++ {
		if raw[index] != '~' {
			result.WriteByte(raw[index])
			continue
		}
		if index+1 >= len(raw) || (raw[index+1] != '0' && raw[index+1] != '1') {
			return "", errors.New("JSON pointer contains an invalid escape")
		}
		index++
		if raw[index] == '0' {
			result.WriteByte('~')
		} else {
			result.WriteByte('/')
		}
	}
	return result.String(), nil
}

func queryDelimited(ctx context.Context, request Request, data []byte) ([]Result, Metadata, contracts.Status, contracts.CountRelation, contracts.Scan, []contracts.Warning, error) {
	if request.Dialect != "csv" && request.Dialect != "tsv" {
		return nil, Metadata{}, "", "", contracts.Scan{}, nil, fmt.Errorf("delimited dialect must be csv or tsv")
	}
	if request.MalformedPolicy == "" {
		request.MalformedPolicy = "fail"
	}
	if request.MalformedPolicy != "fail" && request.MalformedPolicy != "skip_with_warning" {
		return nil, Metadata{}, "", "", contracts.Scan{}, nil, errors.New("malformed_policy must be fail or skip_with_warning")
	}
	for _, filter := range request.Filters {
		if filter.ValueType != "string" {
			return nil, Metadata{}, "", "", contracts.Scan{}, nil, errors.New("delimited filters require value_type=string")
		}
		if err := validateFilter(filter); err != nil {
			return nil, Metadata{}, "", "", contracts.Scan{}, nil, err
		}
	}
	text, err := textcodec.Decode(request.Encoding, data)
	if err != nil {
		return nil, Metadata{}, "", "", contracts.Scan{}, nil, err
	}
	reader := csv.NewReader(strings.NewReader(text))
	if request.Dialect == "tsv" {
		reader.Comma = '\t'
	}
	reader.ReuseRecord = false
	header, err := reader.Read()
	if err != nil {
		return nil, Metadata{}, "", "", contracts.Scan{}, nil, fmt.Errorf("read delimited header: %w", err)
	}
	seen := make(map[string]struct{}, len(header))
	for _, name := range header {
		if name == "" {
			return nil, Metadata{}, "", "", contracts.Scan{}, nil, errors.New("delimited header contains an empty field")
		}
		if _, exists := seen[name]; exists {
			return nil, Metadata{}, "", "", contracts.Scan{}, nil, fmt.Errorf("delimited header %q is duplicated", name)
		}
		seen[name] = struct{}{}
	}
	metadata := Metadata{Mode: "delimited", Dialect: request.Dialect, Encoding: request.Encoding, MissingFieldPolicy: "missing filter fields do not match; projected missing fields are null"}
	results := make([]Result, 0)
	warnings := make([]contracts.Warning, 0)
	status, relation, scanState := contracts.StatusOK, contracts.CountExact, contracts.ScanComplete
	row := uint64(1)
	var projectedBytes uint64
	for {
		if err := ctx.Err(); err != nil {
			status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
			warnings = append(warnings, contracts.Warning{Code: "deadline_exceeded", Message: "delimited scan deadline reached"})
			break
		}
		values, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		row++
		if err != nil {
			if request.MalformedPolicy == "fail" {
				return nil, metadata, "", "", contracts.Scan{}, nil, fmt.Errorf("malformed delimited record near row %d: %w", row, err)
			}
			status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
			warnings = append(warnings, contracts.Warning{Code: "malformed_record", Message: fmt.Sprintf("skipped malformed record near row %d", row)})
			continue
		}
		record := make(map[string]any, len(header))
		for index, name := range header {
			record[name] = values[index]
		}
		if !matchesFilters(record, request.Filters) {
			continue
		}
		projected := project(record, request.Fields)
		encoded, _ := json.Marshal(projected)
		projectedBytes += uint64(len(encoded))
		if len(results) >= MaxDataItems || projectedBytes > MaxProjectedBytes {
			status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
			warnings = append(warnings, contracts.Warning{Code: "projection_limit", Message: "delimited projection limit reached"})
			break
		}
		results = append(results, Result{Kind: request.Dialect + "_record", SourceRef: request.Ref, RecordIndex: row - 1, Record: projected})
	}
	scope := "scope:data:" + hashString(request.Ref+"\x00"+request.Dialect)
	return results, metadata, status, relation, contracts.Scan{State: scanState, ScopeRef: &scope}, warnings, nil
}

func queryLog(ctx context.Context, request Request, data []byte) ([]Result, Metadata, contracts.Status, contracts.CountRelation, contracts.Scan, []contracts.Warning, error) {
	if request.Dialect == "" {
		request.Dialect = "exact"
	}
	metadata := Metadata{Mode: "log", Dialect: request.Dialect, Encoding: request.Encoding}
	if request.Pattern == "" {
		return nil, metadata, "", "", contracts.Scan{}, nil, errors.New("pattern is required for log mode")
	}
	var expression *regexp.Regexp
	if request.Dialect == "regex" {
		pattern := request.Pattern
		if !request.CaseSensitive {
			pattern = "(?i:" + pattern + ")"
		}
		var err error
		expression, err = regexp.Compile(pattern)
		if err != nil {
			return nil, metadata, "", "", contracts.Scan{}, nil, fmt.Errorf("invalid log regular expression: %w", err)
		}
	} else if request.Dialect != "exact" {
		return nil, metadata, "", "", contracts.Scan{}, nil, fmt.Errorf("log dialect must be exact or regex")
	}
	text, err := textcodec.Decode(request.Encoding, data)
	if err != nil {
		return nil, metadata, "", "", contracts.Scan{}, nil, err
	}
	results := make([]Result, 0)
	warnings := make([]contracts.Warning, 0)
	status, relation, scanState := contracts.StatusOK, contracts.CountExact, contracts.ScanComplete
	var matchedBytes uint64
	for index, line := range strings.Split(text, "\n") {
		if err := ctx.Err(); err != nil {
			status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
			warnings = append(warnings, contracts.Warning{Code: "deadline_exceeded", Message: "log scan deadline reached"})
			break
		}
		line = strings.TrimSuffix(line, "\r")
		matched := expression != nil && expression.MatchString(line)
		if expression == nil {
			if request.CaseSensitive {
				matched = strings.Contains(line, request.Pattern)
			} else {
				matched = strings.Contains(strings.ToLower(line), strings.ToLower(request.Pattern))
			}
		}
		if matched {
			matchedBytes += uint64(len(line))
			if matchedBytes > MaxProjectedBytes {
				status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
				warnings = append(warnings, contracts.Warning{Code: "projection_limit", Message: "log projection limit reached"})
				break
			}
			results = append(results, Result{Kind: "log_line", SourceRef: request.Ref, Line: uint64(index + 1), Text: line})
			if len(results) >= MaxDataItems {
				status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
				warnings = append(warnings, contracts.Warning{Code: "match_limit", Message: "log match limit reached"})
				break
			}
		}
	}
	scope := "scope:data:" + hashString(request.Ref+"\x00log\x00"+request.Dialect)
	return results, metadata, status, relation, contracts.Scan{State: scanState, ScopeRef: &scope}, warnings, nil
}

func (s *Service) readSource(ref string) ([]byte, error) {
	path, expectedHash, err := parseSourceRef(ref)
	if err != nil {
		return nil, err
	}
	resolved, err := s.root.ResolveExisting(filepath.FromSlash(path))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("source ref does not identify a regular file")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	data, truncated, readErr := content.ReadBounded(file, MaxSourceBytes)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if truncated {
		return nil, ErrSourceTooLarge
	}
	if expectedHash != "" {
		sum := sha256.Sum256(data)
		if !strings.EqualFold(expectedHash, hex.EncodeToString(sum[:])) {
			return nil, ErrSourceChanged
		}
	}
	return data, nil
}

func parseSourceRef(ref string) (path, hash string, err error) {
	switch {
	case strings.HasPrefix(ref, "source:mutable:"):
		path = strings.TrimPrefix(ref, "source:mutable:")
	case strings.HasPrefix(ref, "source:"):
		value := strings.TrimPrefix(ref, "source:")
		marker := strings.LastIndex(value, "@sha256:")
		if marker < 0 {
			return "", "", errors.New("source ref lacks mutable identity or content hash")
		}
		path, hash = value[:marker], value[marker+len("@sha256:"):]
		if len(hash) != sha256.Size*2 {
			return "", "", errors.New("source ref has invalid SHA-256")
		}
	default:
		return "", "", errors.New("unsupported ref kind")
	}
	if path == "" {
		return "", "", errors.New("source ref has empty path")
	}
	return path, hash, nil
}

func textRange(ctx context.Context, request Request, data []byte) ([]Result, Metadata, error) {
	metadata := Metadata{Mode: "text_range", Encoding: request.Encoding}
	lineMode := request.LineStart != nil || request.LineEnd != nil
	byteMode := request.ByteStart != nil || request.ByteEnd != nil
	if lineMode == byteMode {
		return nil, metadata, errors.New("provide exactly one complete line range or byte range")
	}
	if byteMode {
		if request.ByteStart == nil || request.ByteEnd == nil || *request.ByteEnd < *request.ByteStart || *request.ByteEnd > uint64(len(data)) {
			return nil, metadata, errors.New("invalid byte range")
		}
		if err := ctx.Err(); err != nil {
			return nil, metadata, err
		}
		raw := data[*request.ByteStart:*request.ByteEnd]
		text, decodeErr := textcodec.Decode(request.Encoding, raw)
		result := Result{
			Kind: "byte_range", SourceRef: request.Ref, ByteStart: *request.ByteStart,
			ByteEnd: *request.ByteEnd, BytesBase64: base64.StdEncoding.EncodeToString(raw),
		}
		if decodeErr == nil {
			result.Text = text
		}
		return []Result{result}, metadata, nil
	}
	if request.LineStart == nil || request.LineEnd == nil || *request.LineStart == 0 || *request.LineEnd < *request.LineStart {
		return nil, metadata, errors.New("invalid line range")
	}
	text, err := textcodec.Decode(request.Encoding, data)
	if err != nil {
		return nil, metadata, err
	}
	lines := strings.Split(text, "\n")
	results := make([]Result, 0)
	for line := *request.LineStart; line <= *request.LineEnd && line <= uint64(len(lines)); line++ {
		if err := ctx.Err(); err != nil {
			return nil, metadata, err
		}
		value := strings.TrimSuffix(lines[line-1], "\r")
		results = append(results, Result{Kind: "line", SourceRef: request.Ref, Line: line, Text: value})
		if len(results) >= MaxDataItems {
			return nil, metadata, errors.New("line range exceeds item limit")
		}
	}
	return results, metadata, nil
}

func queryJSONL(ctx context.Context, request Request, data []byte) (
	[]Result, Metadata, contracts.Status, contracts.CountRelation, contracts.Scan, []contracts.Warning, error,
) {
	if request.Dialect == "" {
		request.Dialect = "jsonl-simple"
	}
	if request.Dialect != "jsonl-simple" {
		return nil, Metadata{}, "", "", contracts.Scan{}, nil, fmt.Errorf("unsupported JSONL dialect %q", request.Dialect)
	}
	if request.MalformedPolicy == "" {
		request.MalformedPolicy = "fail"
	}
	if request.MalformedPolicy != "fail" && request.MalformedPolicy != "skip_with_warning" {
		return nil, Metadata{}, "", "", contracts.Scan{}, nil, errors.New("malformed_policy must be fail or skip_with_warning")
	}
	for _, filter := range request.Filters {
		if err := validateFilter(filter); err != nil {
			return nil, Metadata{}, "", "", contracts.Scan{}, nil, err
		}
	}
	metadata := Metadata{
		Mode: "jsonl", Dialect: request.Dialect, Encoding: request.Encoding,
		MissingFieldPolicy: "projected missing fields are returned as null; missing filter fields do not match",
	}
	text, err := textcodec.Decode(request.Encoding, data)
	if err != nil {
		return nil, metadata, "", "", contracts.Scan{}, nil, err
	}
	results := make([]Result, 0)
	warnings := make([]contracts.Warning, 0)
	status := contracts.StatusOK
	relation := contracts.CountExact
	scanState := contracts.ScanComplete
	var projectedBytes uint64
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64*1024), int(contracts.MaximumJSONLRecord))
	line := uint64(0)
	for scanner.Scan() {
		line++
		if err := ctx.Err(); err != nil {
			status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
			warnings = append(warnings, contracts.Warning{Code: "deadline_exceeded", Message: "JSONL scan deadline reached"})
			break
		}
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			if request.MalformedPolicy == "fail" {
				return nil, metadata, "", "", contracts.Scan{}, nil, fmt.Errorf("malformed JSONL record at line %d: %w", line, err)
			}
			status, relation = contracts.StatusPartial, contracts.CountLowerBound
			warnings = append(warnings, contracts.Warning{Code: "malformed_record", Message: fmt.Sprintf("skipped malformed record at line %d", line)})
			continue
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if request.MalformedPolicy == "fail" {
				return nil, metadata, "", "", contracts.Scan{}, nil, fmt.Errorf("malformed JSONL record at line %d: trailing JSON value or data", line)
			}
			status, relation = contracts.StatusPartial, contracts.CountLowerBound
			warnings = append(warnings, contracts.Warning{Code: "malformed_record", Message: fmt.Sprintf("skipped malformed record at line %d", line)})
			continue
		}
		if !matchesFilters(record, request.Filters) {
			continue
		}
		projected := project(record, request.Fields)
		encoded, err := json.Marshal(projected)
		if err != nil {
			return nil, metadata, "", "", contracts.Scan{}, nil, err
		}
		projectedBytes += uint64(len(encoded))
		if len(results) >= MaxDataItems || projectedBytes > MaxProjectedBytes {
			status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
			warnings = append(warnings, contracts.Warning{Code: "projection_limit", Message: "JSONL projection limit reached"})
			break
		}
		results = append(results, Result{Kind: "jsonl_record", SourceRef: request.Ref, Line: line, Record: projected})
	}
	if err := scanner.Err(); err != nil {
		if request.MalformedPolicy == "fail" {
			return nil, metadata, "", "", contracts.Scan{}, nil, fmt.Errorf("read JSONL: %w", err)
		}
		status, relation, scanState = contracts.StatusPartial, contracts.CountLowerBound, contracts.ScanPartial
		warnings = append(warnings, contracts.Warning{Code: "oversized_record", Message: "JSONL record exceeded the record limit"})
	}
	scope := "scope:data:" + hashString(request.Ref+"\x00"+request.Dialect)
	return results, metadata, status, relation, contracts.Scan{State: scanState, ScopeRef: &scope}, warnings, nil
}

func validateFilter(filter Filter) error {
	if filter.Field == "" {
		return errors.New("filter field is required")
	}
	switch filter.ValueType {
	case "string":
		return nil
	case "boolean":
		_, err := strconv.ParseBool(filter.Value)
		return err
	case "null":
		return nil
	case "integer":
		if _, ok := new(big.Int).SetString(filter.Value, 10); !ok {
			return fmt.Errorf("invalid integer filter %q", filter.Value)
		}
		return nil
	default:
		return fmt.Errorf("unsupported filter value_type %q", filter.ValueType)
	}
}

func matchesFilters(record map[string]any, filters []Filter) bool {
	for _, filter := range filters {
		value, ok := record[filter.Field]
		if !ok {
			return false
		}
		switch filter.ValueType {
		case "string":
			actual, ok := value.(string)
			if !ok || actual != filter.Value {
				return false
			}
		case "boolean":
			expected, _ := strconv.ParseBool(filter.Value)
			actual, ok := value.(bool)
			if !ok || actual != expected {
				return false
			}
		case "null":
			if value != nil {
				return false
			}
		case "integer":
			actual, ok := value.(json.Number)
			actualInteger, actualOK := new(big.Int).SetString(actual.String(), 10)
			expectedInteger, _ := new(big.Int).SetString(filter.Value, 10)
			if !ok || !actualOK || actualInteger.Cmp(expectedInteger) != 0 {
				return false
			}
		}
	}
	return true
}

func project(record map[string]any, fields []string) map[string]any {
	if len(fields) == 0 {
		return record
	}
	result := make(map[string]any, len(fields))
	for _, field := range fields {
		result[field] = record[field]
	}
	return result
}

func completeMetadata(metadata Metadata) persistedMetadata {
	scope := "scope:data:" + hashString(metadata.Mode+"\x00"+metadata.Encoding)
	return persistedMetadata{
		Status: contracts.StatusOK, Relation: contracts.CountExact,
		Scan:     contracts.Scan{State: contracts.ScanNotApplicable, ScopeRef: &scope},
		Warnings: contracts.EmptyWarnings(), Metadata: metadata,
	}
}

func unsupportedResponse(request Request, err error) Response {
	return Response{
		Status: contracts.StatusUnsupported, Items: make([]Result, 0),
		Counts:   contracts.Counts{Relation: contracts.CountUnknown},
		Scan:     contracts.Scan{State: contracts.ScanNotApplicable},
		Warnings: []contracts.Warning{{Code: "unsupported_encoding", Message: err.Error()}},
		Metadata: Metadata{Mode: request.Mode, Encoding: request.Encoding},
	}
}

func (s *Service) persist(ctx context.Context, request Request, sourceData []byte, results []Result, metadata persistedMetadata, itemLimit, byteLimit uint64) (Response, error) {
	id, err := resultID()
	if err != nil {
		return Response{}, err
	}
	now := s.now().UTC()
	sourcePath, _, err := parseSourceRef(request.Ref)
	if err != nil {
		return Response{}, err
	}
	metadata.SourceIdentity = "source:" + filepath.ToSlash(sourcePath) + "@sha256:" + hashBytes(sourceData)
	metadataJSON, _ := json.Marshal(metadata)
	request.Cursor = ""
	requestJSON, _ := json.Marshal(request)
	set := store.ResultSet{
		ID: id, WorkspaceID: s.root.ID(), QueryHash: "sha256:" + hashBytes(requestJSON),
		GenerationID: "data-query-v1", CreatedAt: now, ExpiresAt: now.Add(resultTTL), Metadata: metadataJSON,
		Items: make([]store.ResultItem, 0, len(results)),
	}
	for index, result := range results {
		payload, _ := json.Marshal(result)
		set.Items = append(set.Items, store.ResultItem{
			Ordinal: uint64(index), ItemRef: result.SourceRef,
			ItemHash: "sha256:" + hashBytes(payload), Payload: payload,
		})
	}
	if err := s.repository.CreateResultSet(ctx, set); err != nil {
		return Response{}, err
	}
	return s.readPage(ctx, id, 0, itemLimit, byteLimit, set.ExpiresAt)
}

func (s *Service) fromCursor(ctx context.Context, encoded string, itemLimit, byteLimit uint64) (Response, error) {
	payload, err := s.cursors.Decode(encoded, s.root.ID())
	if err != nil {
		return Response{}, err
	}
	return s.readPage(ctx, payload.ResultSetID, payload.NextOrdinal, itemLimit, byteLimit, time.Unix(payload.ExpiresUnix, 0))
}

func (s *Service) readPage(ctx context.Context, id string, from, itemLimit, byteLimit uint64, expires time.Time) (Response, error) {
	page, err := s.repository.ReadResultPage(ctx, id, from, itemLimit)
	if err != nil {
		return Response{}, err
	}
	var metadata persistedMetadata
	if err := json.Unmarshal(page.Metadata, &metadata); err != nil {
		return Response{}, err
	}
	if metadata.SourceIdentity == "" {
		return Response{}, ErrSourceChanged
	}
	if _, err := s.readSource(metadata.SourceIdentity); err != nil {
		return Response{}, fmt.Errorf("%w: source no longer matches the paginated result", ErrSourceChanged)
	}
	items := make([]Result, 0, len(page.Items))
	for _, stored := range page.Items {
		var item Result
		decoder := json.NewDecoder(bytes.NewReader(stored.Payload))
		decoder.UseNumber()
		if err := decoder.Decode(&item); err != nil {
			return Response{}, err
		}
		items = append(items, item)
	}
	for returned := len(items); returned >= 0; returned-- {
		nextOrdinal := from + uint64(returned)
		knownMore := nextOrdinal < page.Total
		var next *string
		if knownMore {
			value, err := s.cursors.Encode(s.root.ID(), id, nextOrdinal, expires)
			if err != nil {
				return Response{}, err
			}
			next = &value
		}
		var truncated *bool
		if knownMore {
			value := true
			truncated = &value
		} else if metadata.Scan.State == contracts.ScanPartial {
			truncated = nil
		} else {
			value := false
			truncated = &value
		}
		matched := page.Total
		snapshot := "snapshot:" + id
		response := Response{
			Status: metadata.Status, Items: items[:returned],
			Counts: contracts.Counts{Matched: &matched, Relation: metadata.Relation, Returned: uint64(returned)},
			Scan:   metadata.Scan, Truncated: truncated, NextCursor: next, SnapshotRef: &snapshot,
			Warnings: metadata.Warnings, Metadata: metadata.Metadata,
		}
		encoded, err := json.Marshal(response)
		if err == nil && uint64(len(encoded)) <= byteLimit {
			if returned == 0 && len(items) > 0 {
				return Response{}, ErrResponseTooLarge
			}
			return response, nil
		}
	}
	return Response{}, ErrResponseTooLarge
}

func resultID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "data_" + hex.EncodeToString(data), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashString(value string) string { return hashBytes([]byte(value)) }
