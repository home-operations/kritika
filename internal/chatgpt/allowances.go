package chatgpt

import (
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Allowance is one upstream quota bucket, observed on an inference response.
type Allowance struct {
	LimitID    string           `json:"limitId"`
	Primary    *AllowanceWindow `json:"primary"`
	Secondary  *AllowanceWindow `json:"secondary"`
	ObservedAt time.Time        `json:"observedAt"`
}

// AllowanceWindow keeps unknown durations and reset times distinct from zero.
type AllowanceWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

// AllowancesFromHeaders reads the quota header families used by Codex.
// Ordinary API request/token rate limits are separate from plan allowances.
func AllowancesFromHeaders(headers http.Header, now time.Time) []Allowance {
	ids := map[string]bool{}
	for name := range headers {
		name = strings.ToLower(name)
		for _, suffix := range []string{"-primary-used-percent", "-secondary-used-percent"} {
			if strings.HasPrefix(name, "x-") && strings.HasSuffix(name, suffix) {
				ids[strings.TrimSuffix(strings.TrimPrefix(name, "x-"), suffix)] = true
			}
		}
	}
	var out []Allowance
	for id := range ids {
		prefix := "x-" + id
		a := Allowance{
			LimitID: strings.ReplaceAll(id, "-", "_"), ObservedAt: now,
			Primary: headerWindow(headers, prefix+"-primary"), Secondary: headerWindow(headers, prefix+"-secondary"),
		}
		if a.Primary != nil || a.Secondary != nil {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b Allowance) int { return strings.Compare(a.LimitID, b.LimitID) })
	return out
}

func headerWindow(headers http.Header, prefix string) *AllowanceWindow {
	used, err := strconv.ParseFloat(headers.Get(prefix+"-used-percent"), 64)
	if err != nil || !validPercent(used) {
		return nil
	}
	return &AllowanceWindow{
		UsedPercent: used, WindowDurationMins: positiveInt(headers.Get(prefix + "-window-minutes")),
		ResetsAt: positiveInt(headers.Get(prefix + "-reset-at")),
	}
}

func positiveInt(raw string) *int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

func validPercent(n float64) bool { return n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }

// AllowanceFromEvent reads a codex.rate_limits SSE event when one is supplied.
func AllowanceFromEvent(raw string, now time.Time) (Allowance, bool) {
	type window struct {
		UsedPercent   *float64 `json:"used_percent"`
		WindowMinutes *int64   `json:"window_minutes"`
		ResetAt       *int64   `json:"reset_at"`
	}
	var event struct {
		Type             string `json:"type"`
		MeteredLimitName string `json:"metered_limit_name"`
		LimitName        string `json:"limit_name"`
		RateLimits       struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal([]byte(raw), &event) != nil || event.Type != "codex.rate_limits" {
		return Allowance{}, false
	}
	convert := func(w *window) *AllowanceWindow {
		if w == nil || w.UsedPercent == nil || !validPercent(*w.UsedPercent) {
			return nil
		}
		out := &AllowanceWindow{UsedPercent: *w.UsedPercent}
		if w.WindowMinutes != nil && *w.WindowMinutes > 0 {
			out.WindowDurationMins = w.WindowMinutes
		}
		if w.ResetAt != nil && *w.ResetAt > 0 {
			out.ResetsAt = w.ResetAt
		}
		return out
	}
	id := event.MeteredLimitName
	if id == "" {
		id = event.LimitName
	}
	if id == "" {
		id = "codex"
	}
	a := Allowance{LimitID: strings.ReplaceAll(id, "-", "_"), ObservedAt: now,
		Primary: convert(event.RateLimits.Primary), Secondary: convert(event.RateLimits.Secondary)}
	return a, a.Primary != nil || a.Secondary != nil
}
