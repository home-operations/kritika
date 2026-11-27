package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/tuning"
)

// TestEncodeFollowsToolResultCap: a tool result is cut at tuning's
// Transcript.ToolResultCap as the process has it.
func TestEncodeFollowsToolResultCap(t *testing.T) {
	content := strings.Repeat("x", 300)
	encode := func() ToolResult {
		t.Helper()
		r := delta(State{}, stepReq("s", result(content)))
		r.Response = Response{Text: "answer", Stop: model.StopEndTurn}
		var msgs []Message
		if err := json.Unmarshal(r.Encode().Messages, &msgs); err != nil {
			t.Fatal(err)
		}
		return msgs[0].ToolResults[0]
	}
	if got := encode(); got.Content != content || got.TruncatedBytes != 0 {
		t.Fatalf("at the default cap: %d bytes, %d cut; want it whole", len(got.Content), got.TruncatedBytes)
	}
	tuning.Override(t, func(v *tuning.Values) { v.Transcript.ToolResultCap = 100 })
	if got := encode(); len(got.Content) != 100 || got.TruncatedBytes != 200 {
		t.Fatalf("at a cap of 100: %d bytes, %d cut; want 100 and 200", len(got.Content), got.TruncatedBytes)
	}
}
