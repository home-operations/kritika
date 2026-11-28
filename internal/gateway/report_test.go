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

// TestDecodePartsReportPastTheSmallReport: a remaining-parts report of
// eight long pool names, over a lowered gateway.maxSmallReport, is held to
// gateway.maxPartsReport alone.
func TestDecodePartsReportPastTheSmallReport(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Runner.MaxFailureCause, v.Gateway.MaxSmallReport = 64, 6*64+1<<10 })
	report := runspec.PartsReport{Sequence: 1, Remaining: map[string]int{}, Active: map[string]int{}}
	for i := range 8 {
		pool := strconv.Itoa(i) + strings.Repeat("m", 100)
		report.Remaining[pool], report.Active[pool] = 2, 1
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) <= int(tuning.Get().Gateway.MaxSmallReport) {
		t.Fatalf("the report is %d bytes, within the small report's bound", len(b))
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, runspec.ReportPartsPath, strings.NewReader(string(b)))
	if _, ok := decodeReport[runspec.PartsReport](w, r, tuning.Get().Gateway.MaxPartsReport); !ok {
		t.Fatalf("status %d, %s", w.Code, w.Body)
	}
}
