package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/model"
)

func TestReplaceJSONNUL(t *testing.T) {
	for _, tt := range []struct{ name, in, want string }{
		{"nul", `"a\u0000b"`, `"a\ufffdb"`},
		{"literal escape", `"a\\u0000b"`, `"a\\u0000b"`},
		{"backslash then nul", `"a\\\u0000b"`, `"a\\\ufffdb"`},
		{"nested", `{"\u0000":["\u0000",9007199254740993,1.00,"\\u0000"]}`, `{"\ufffd":["\ufffd",9007199254740993,1.00,"\\u0000"]}`},
		{"unicode", `"hello 世界\n\u0001"`, `"hello 世界\n\u0001"`},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(tt.in)
			replaceJSONNUL(raw)
			if string(raw) != tt.want {
				t.Fatalf("got %s, want %s", raw, tt.want)
			}
		})
	}
}

func TestEncodeNUL(t *testing.T) {
	req := model.StepRequest{
		System:   "system\x00text",
		Messages: []model.Message{{Role: model.RoleUser, Text: "text\x00end"}},
		Tools:    []model.ToolDef{{Name: "tool", InputSchema: json.RawMessage(`{"description":"\u0000","literal":"\\u0000"}`)}},
	}
	resp := model.StepResponse{Text: "response\x00end", ToolCalls: []model.ToolCall{{ID: "call", Name: "tool", Input: json.RawMessage(`{"text":"\u0000"}`)}}}
	row := Delta(State{}, req, nil)
	row.Response = NewResponse(resp, nil)
	encoded := row.Encode()
	if *encoded.System != "system\ufffdtext" || req.System != "system\x00text" || *row.System != req.System {
		t.Fatalf("system = %q, original = %q", *encoded.System, req.System)
	}
	for _, raw := range []json.RawMessage{encoded.Messages, encoded.Tools, encoded.Response} {
		if !json.Valid(raw) || !strings.Contains(string(raw), `\ufffd`) {
			t.Fatalf("encoded JSON = %s", raw)
		}
	}
	if !strings.Contains(string(encoded.Tools), `"literal":"\\u0000"`) {
		t.Fatalf("literal escape changed: %s", encoded.Tools)
	}
	if want := len(*encoded.System) + len(encoded.Tools) + len(encoded.Messages) + len(encoded.Response); encoded.State.Bytes != int64(want) {
		t.Fatalf("bytes = %d, want %d", encoded.State.Bytes, want)
	}
	next := Delta(encoded.State, req, nil)
	if next.Reset || next.MessagesFrom != 1 || next.System != nil || next.Tools != nil {
		t.Fatalf("NUL replacement broke delta state: %+v", next)
	}
}
