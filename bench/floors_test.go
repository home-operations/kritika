//go:build bench

package bench

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFloorsFrom(t *testing.T) {
	a := report{Judge: "vendor/judge", Modes: map[string]modeTotals{
		"diff":    {MustRecall: 0.6, MeanFindings: 4, MeanToolCalls: 10},
		"context": {MustRecall: 0.8, MeanFindings: 6, MeanToolCalls: 12},
	}}
	b := report{Judge: "vendor/judge", Modes: map[string]modeTotals{
		"diff": {MustRecall: 0.5, MeanFindings: 2, MeanToolCalls: 8},
	}}
	f, err := FloorsFrom(a, b)
	if err != nil {
		t.Fatal(err)
	}
	// diff: mean 0.55, sigma 0.05, floor 0.55 - 0.11. The collapse floors
	// are half the means, 3 and 9. context ran once: its sigma is 0.
	want := Floors{Judge: "vendor/judge", Modes: map[string]ModeFloors{
		"diff":    {MustRecall: 0.44, MeanFindings: 1.5, MeanToolCalls: 4.5},
		"context": {MustRecall: 0.8, MeanFindings: 3, MeanToolCalls: 6},
	}}
	if f.Judge != want.Judge || len(f.Modes) != 2 || f.Modes["diff"] != want.Modes["diff"] || f.Modes["context"] != want.Modes["context"] {
		t.Fatalf("floors = %+v, want %+v", f, want)
	}
	if _, err := FloorsFrom(a, report{Judge: "other"}); err == nil {
		t.Error("runs scored by different judges set floors")
	}
	if _, err := FloorsFrom(); err == nil {
		t.Error("no runs set floors")
	}
	if f, err := FloorsFrom(report{Modes: map[string]modeTotals{"diff": {MustRecall: 0.1}}}, report{Modes: map[string]modeTotals{"diff": {MustRecall: 0.9}}}); err != nil ||
		f.Modes["diff"].MustRecall != 0 {
		t.Errorf("a floor under zero = %+v, %v; want 0", f, err)
	}
	// Two runs at two thirds set a floor two thirds meets again.
	third := report{Judge: "j", Modes: map[string]modeTotals{"diff": {MustRecall: 2.0 / 3}}}
	f, err = FloorsFrom(third, third)
	if err != nil {
		t.Fatal(err)
	}
	if under, gated := f.Check(third); !gated || under != nil {
		t.Errorf("a run at its own baseline is under its floor: %q", under)
	}
}

func TestFloorsFromRejectsNonLive(t *testing.T) {
	live := report{Judge: "judge", Modes: map[string]modeTotals{"diff": {MustRecall: 0.8}}}
	dry, scripted := live, live
	dry.Dry, scripted.Scripted = true, true
	for _, tt := range []struct {
		name string
		runs []report
		want string
	}{
		{"dry", []report{dry, dry}, "run 1 is dry or scripted"},
		{"scripted", []report{scripted, scripted}, "run 1 is dry or scripted"},
		{"live then dry", []report{live, dry}, "run 2 is dry or scripted"},
		{"live then scripted", []report{live, scripted}, "run 2 is dry or scripted"},
		{"dry then live", []report{dry, live}, "run 1 is dry or scripted"},
		{"scripted then live", []report{scripted, live}, "run 1 is dry or scripted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, err := FloorsFrom(tt.runs...)
			if err == nil || !strings.Contains(err.Error(), tt.want) || f.Modes != nil {
				t.Fatalf("FloorsFrom = %+v, %v; want no floors and %q", f, err, tt.want)
			}
		})
	}
}

func TestFloorsCheck(t *testing.T) {
	f := Floors{Judge: "vendor/judge", Modes: map[string]ModeFloors{
		"diff":    {MustRecall: 0.5, MeanFindings: 2, MeanToolCalls: 4},
		"overlay": {MustRecall: 0.9},
	}}
	rep := report{Judge: "vendor/judge", Modes: map[string]modeTotals{
		"diff":    {MustRecall: 0.5, MeanFindings: 1.5, MeanToolCalls: 3},
		"context": {MustRecall: 0.1},
	}}
	under, gated := f.Check(rep)
	want := []string{"diff: mean findings 1.500 is under its floor 2.000", "diff: mean tool calls 3.000 is under its floor 4.000"}
	if !gated || !slices.Equal(under, want) {
		t.Fatalf("Check = %q, %v; want %q: a mode the run did not make is not checked, and a floor met is not under", under, gated, want)
	}
	rep.Judge = ""
	if under, gated := f.Check(rep); gated || under != nil {
		t.Fatalf("a run scored by another judge is held to the floors: %q, %v", under, gated)
	}
}

func TestLoadFloors(t *testing.T) {
	dir := t.TempDir()
	if _, found, err := LoadFloors(filepath.Join(dir, "none.json")); err != nil || found {
		t.Fatalf("an absent file = %v, %v", found, err)
	}
	path := filepath.Join(dir, "floors.json")
	if err := os.WriteFile(path, []byte(`{"judge":"","modes":{"diff":{"must_recall":0.4,"mean_findings":1,"mean_tool_calls":2}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, found, err := LoadFloors(path)
	if err != nil || !found || f.Modes["diff"] != (ModeFloors{MustRecall: 0.4, MeanFindings: 1, MeanToolCalls: 2}) {
		t.Fatalf("loaded = %+v, %v, %v", f, found, err)
	}
	if err := os.WriteFile(path, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadFloors(path); err == nil {
		t.Error("a broken file loaded")
	}
}
