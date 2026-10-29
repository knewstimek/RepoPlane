package resultview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRenderRemovesOnlyEquivalentPlainJSONEcho(t *testing.T) {
	input := []byte(`{
		"structuredContent":{"id":"9007199254740993","validity":"current","warnings":[{"code":"memo_basis_older"}],"n":9007199254740993},
		"content":[
			{"type":"text","text":"{\"n\":9007199254740993,\"warnings\":[{\"code\":\"memo_basis_older\"}],\"validity\":\"current\",\"id\":\"9007199254740993\"}"},
			{"type":"text","text":"Verify current source before applying."},
			{"type":"image","data":"AA==","mimeType":"image/png"}
		],
		"isError":true,
		"_meta":{"repoplane/usage.v1":{"structured_bytes":123}},
		"requestState":"approval-token",
		"inputRequests":{"approval":{"message":"Review first"}},
		"extension":{"exact":9007199254740993}
	}`)
	output, err := Render(input)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(input, &before)
	_ = json.Unmarshal(output, &after)
	var blocks []json.RawMessage
	_ = json.Unmarshal(after["content"], &blocks)
	if len(blocks) != 2 || !strings.Contains(string(blocks[0]), "Verify current source") ||
		!strings.Contains(string(blocks[1]), "image/png") {
		t.Fatalf("unique content was lost: %s", output)
	}
	for key, value := range before {
		if key == "content" {
			continue
		}
		want, err := comparableJSON(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := comparableJSON(after[key])
		if err != nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("%s changed: before=%s after=%s", key, value, after[key])
		}
	}
	if len(output) >= len(input) {
		t.Fatalf("duplicate removal did not reduce output: %d >= %d", len(output), len(input))
	}
	again, err := Render(output)
	if err != nil || string(again) != string(output) {
		t.Fatalf("render is not idempotent: %s %v", again, err)
	}
}

func TestRenderPreservesNonredundantContent(t *testing.T) {
	tests := map[string]string{
		"rounded BIGINT":     `{"structuredContent":{"n":9007199254740993},"content":[{"type":"text","text":"{\"n\":9007199254740992}"}]}`,
		"annotated echo":     `{"structuredContent":{"n":1},"content":[{"type":"text","text":"{\"n\":1}","annotations":{"audience":["user"]}}]}`,
		"extended echo":      `{"structuredContent":{"n":1},"content":[{"type":"text","text":"{\"n\":1}","extra":true}]}`,
		"JSON prefix":        `{"structuredContent":{"n":1},"content":[{"type":"text","text":"{\"n\":1} warning"}]}`,
		"different numbers":  `{"structuredContent":{"n":1},"content":[{"type":"text","text":"{\"n\":1.0}"}]}`,
		"duplicate text key": `{"structuredContent":{"n":1},"content":[{"type":"text","text":"{\"n\":2,\"n\":1}"}]}`,
		"text only error":    `{"isError":true,"content":[{"type":"text","text":"permission denied"}]}`,
		"null structured":    `{"structuredContent":null,"content":[{"type":"text","text":"null"}]}`,
		"empty structured":   `{"structuredContent":{},"content":[{"type":"text","text":"important explanation"}]}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			output, err := Render([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			want, _ := comparableJSON([]byte(input))
			got, _ := comparableJSON(output)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("nonredundant response changed: %s", output)
			}
		})
	}
}

func TestRenderPreservesAmbiguousEnvelope(t *testing.T) {
	for _, input := range []string{
		`{"structuredContent":{"n":2,"n":1},"content":[{"type":"text","text":"{\"n\":1}"}]}`,
		`{"structuredContent":{"n":1},"isError":true,"isError":false,"content":[{"type":"text","text":"{\"n\":1}"}]}`,
	} {
		output, err := Render([]byte(input))
		if err != nil || string(output) != input {
			t.Fatalf("ambiguous envelope changed: %s %v", output, err)
		}
	}
}

func TestRenderRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{"", "null", "[]", "{}", `{"result":{}}`, `{"content":[]}{ }`, `{"content":"bad"}`, "{\"content\":[{\"type\":\"text\",\"text\":\"\xff\"}]}", `{"structuredContent":{},"content":"bad"}`} {
		if _, err := Render([]byte(input)); err == nil {
			t.Fatalf("accepted invalid input: %s", input)
		}
	}
}

func TestRenderKeepsStructuredDataWhenAllTextIsDuplicate(t *testing.T) {
	output, err := Render([]byte(`{"structuredContent":{"scope":"example","source":"observed"},"content":[{"type":"text","text":"{\"scope\":\"example\",\"source\":\"observed\"}"}]}`))
	if err != nil || !strings.Contains(string(output), `"content":[]`) ||
		!strings.Contains(string(output), `"source":"observed"`) {
		t.Fatalf("result=%s err=%v", output, err)
	}
}
