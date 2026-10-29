// Package resultview identifies equivalent MCP JSON text echoes without
// discarding nonredundant content or rounding JSON numbers.
package resultview

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"
)

// Render preserves the CallToolResult envelope, dropping only plain text blocks
// whose JSON equals structuredContent. Numbers retain their original precision.
func Render(input []byte) ([]byte, error) {
	if !utf8.Valid(input) {
		return nil, errors.New("render-result requires UTF-8 JSON")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(input, &result); err != nil || result == nil {
		return nil, errors.New("render-result requires a JSON CallToolResult object")
	}
	content, hasContent := result["content"]
	structured, hasStructured := result["structuredContent"]
	if !hasContent && !hasStructured {
		return nil, errors.New("render-result requires content or structuredContent")
	}
	if _, err := comparableJSON(input); err != nil {
		// Preserve an ambiguous envelope byte for byte instead of normalizing it.
		return bytes.TrimSpace(input), nil
	}
	var blocks []json.RawMessage
	if hasContent {
		if err := json.Unmarshal(content, &blocks); err != nil {
			return nil, errors.New("render-result content must be an array")
		}
	}
	if !hasStructured || bytes.Equal(bytes.TrimSpace(structured), []byte("null")) || !hasContent {
		return json.Marshal(result)
	}
	duplicates := DuplicateTextBlocks(structured, blocks)
	kept := make([]json.RawMessage, 0, len(blocks))
	removed := false
	for i, block := range blocks {
		if duplicates[i] {
			removed = true
			continue
		}
		kept = append(kept, block)
	}
	if removed {
		result["content"], _ = json.Marshal(kept)
	}
	return json.Marshal(result)
}

// DuplicateTextBlocks marks only plain text blocks containing the same JSON as
// structured. Callers retain the original blocks and envelope, avoiding a typed
// JSON round trip. Ambiguous JSON and annotated/extended blocks are kept.
func DuplicateTextBlocks(structured []byte, blocks []json.RawMessage) []bool {
	duplicates := make([]bool, len(blocks))
	if !utf8.Valid(structured) || bytes.Equal(bytes.TrimSpace(structured), []byte("null")) {
		return duplicates
	}
	value, err := comparableJSON(structured)
	if err != nil {
		return duplicates
	}
	for i, block := range blocks {
		if !utf8.Valid(block) {
			continue
		}
		if _, err := comparableJSON(block); err != nil {
			continue
		}
		var fields map[string]json.RawMessage
		var kind, text string
		if json.Unmarshal(block, &fields) == nil && len(fields) == 2 &&
			json.Unmarshal(fields["type"], &kind) == nil && kind == "text" &&
			json.Unmarshal(fields["text"], &text) == nil {
			echo, err := comparableJSON([]byte(text))
			duplicates[i] = err == nil && reflect.DeepEqual(value, echo)
		}
	}
	return duplicates
}

// comparableJSON rejects duplicate keys rather than assuming that discarding
// an earlier value is safe. UseNumber avoids rounding BIGINTs during comparison.
func comparableJSON(data []byte) (any, error) {
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("expected one JSON value")
	}
	return value, nil
}

func readValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	if delim == '{' {
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("expected object key")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate object key")
			}
			object[name], err = readValue(decoder)
			if err != nil {
				return nil, err
			}
		}
		_, err := decoder.Token()
		return object, err
	}
	if delim == '[' {
		array := make([]any, 0)
		for decoder.More() {
			value, err := readValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err := decoder.Token()
		return array, err
	}
	return nil, errors.New("unexpected JSON delimiter")
}
