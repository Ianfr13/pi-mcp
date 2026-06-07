package parser

import (
	"encoding/json"
	"testing"
)

func TestExtractWorkflowResult(t *testing.T) {
	const fenced = "✓ Workflow \"judge_claims\" finished (4 agents · 120,469 tokens · $0.1464 · 22.4s).\n" +
		"```json\n{\n  \"overall\": \"ok\",\n  \"claims\": []\n}\n```"
	const noHeaderFenced = "```json\n{\"a\":1}\n```"
	const rawOnly = "just a plain sentence with no json block"
	const headerThenRaw = "✓ Workflow \"x\" finished (1 agents).\nplain text after header, no fence"

	tests := []struct {
		name      string
		in        string
		wantJSON  string // expected RawMessage as compact JSON; "" means "any string-wrapped"
		wantKeys  []string
		isString  bool   // true when result is a JSON string fallback
		wantStrEq string // expected decoded string value when isString
	}{
		{
			name:     "header + fenced json (fixture shape)",
			in:       fenced,
			wantKeys: []string{"overall", "claims"},
		},
		{
			name:     "fenced json without header line",
			in:       noHeaderFenced,
			wantJSON: `{"a":1}`,
		},
		{
			name:      "no fence -> raw text fallback as JSON string",
			in:        rawOnly,
			isString:  true,
			wantStrEq: rawOnly,
		},
		{
			name:      "header but no fence -> body after header as JSON string",
			in:        headerThenRaw,
			isString:  true,
			wantStrEq: "plain text after header, no fence",
		},
		{
			name:      "empty -> empty JSON string",
			in:        "",
			isString:  true,
			wantStrEq: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractWorkflowResult(tc.in)
			if !json.Valid(got) {
				t.Fatalf("extractWorkflowResult produced invalid JSON: %q", string(got))
			}
			if tc.isString {
				var s string
				if err := json.Unmarshal(got, &s); err != nil {
					t.Fatalf("expected JSON string, got %q (%v)", string(got), err)
				}
				if s != tc.wantStrEq {
					t.Fatalf("string fallback = %q, want %q", s, tc.wantStrEq)
				}
				return
			}
			if tc.wantJSON != "" {
				var a, b interface{}
				_ = json.Unmarshal(got, &a)
				_ = json.Unmarshal([]byte(tc.wantJSON), &b)
				ga, _ := json.Marshal(a)
				gb, _ := json.Marshal(b)
				if string(ga) != string(gb) {
					t.Fatalf("json = %s, want %s", ga, gb)
				}
			}
			for _, k := range tc.wantKeys {
				var m map[string]json.RawMessage
				if err := json.Unmarshal(got, &m); err != nil {
					t.Fatalf("expected object, got %q", string(got))
				}
				if _, ok := m[k]; !ok {
					t.Fatalf("result missing key %q in %s", k, string(got))
				}
			}
		})
	}
}
