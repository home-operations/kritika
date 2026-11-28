package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/tuning"
)

// TestDecodeSmallReportsAtTheirBound: at the least gateway.maxSmallReport
// that the validation takes, a run's longest phase report and a failure
// report of runner.maxFailureCause escaped at worst are decoded, and a
// report over the bound is refused as too large with the limit named.
func TestDecodeSmallReportsAtTheirBound(t *testing.T) {
	v := tuning.Defaults()
	v.Runner.MaxFailureCause = 64
	v.Gateway.MaxSmallReport = 6*64 + 1<<10 - 1
	if v.Validate() == nil {
		t.Fatal("a bound under the failure report validated")
	}
	v.Gateway.MaxSmallReport++
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	tuning.Override(t, func(o *tuning.Values) { *o = v })
	decode := func(body any) *httptest.ResponseRecorder {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, runspec.ReportPhasePath, strings.NewReader(string(b)))
		switch body.(type) {
		case runspec.PhaseReport:
			decodeReport[runspec.PhaseReport](w, r, v.Gateway.MaxSmallReport)
		default:
			decodeReport[runspec.FailureReport](w, r, v.Gateway.MaxSmallReport)
		}
		return w
	}
	for _, body := range []any{
		runspec.PhaseReport{Phase: "reviewing"},
		runspec.FailureReport{Cause: strings.Repeat("<", int(v.Runner.MaxFailureCause))},
	} {
		if w := decode(body); w.Code != http.StatusOK {
			t.Fatalf("%+v: status %d, %s", body, w.Code, w.Body)
		}
	}
	w := decode(runspec.FailureReport{Cause: strings.Repeat("x", int(v.Gateway.MaxSmallReport))})
	if w.Code != http.StatusRequestEntityTooLarge || w.Header().Get(runspec.ReportLimitHeader) != strconv.FormatInt(int64(v.Gateway.MaxSmallReport), 10) {
		t.Fatalf("status %d, %s %q", w.Code, runspec.ReportLimitHeader, w.Header().Get(runspec.ReportLimitHeader))
	}
}
