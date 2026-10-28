//go:build bench

package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
)

// Floors are the least a run may measure before the bench fails, per
// mode: the recall of the must-find cases, and two collapse floors that
// trip when the engine breaks rather than on noise, the mean findings and
// the mean tool calls per case, set at about half of what runs measured.
// They are pinned to the judge that scored the runs they were set from,
// since a judge scores differently from lines alone and from another
// judge: a run scored otherwise is not held to them.
type Floors struct {
	// Judge is the judge the floors were set with, as judge.Name says,
	// "" for lines alone.
	Judge string                `json:"judge"`
	Modes map[string]ModeFloors `json:"modes"`
}

// ModeFloors are one mode's floors.
type ModeFloors struct {
	MustRecall    float64 `json:"must_recall"`
	MeanFindings  float64 `json:"mean_findings"`
	MeanToolCalls float64 `json:"mean_tool_calls"`
}

// floorSigmas is how many standard deviations under the mean of the runs
// a recall floor is set, Kodus's figure, so two runs that differ by noise
// set a floor under both.
const floorSigmas = 2.2

// collapseShare is the share of the measured mean a collapse floor is set
// at.
const collapseShare = 0.5

// LoadFloors reads the floors at path; none when the file is absent.
func LoadFloors(path string) (Floors, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Floors{}, false, nil
	}
	if err != nil {
		return Floors{}, false, err
	}
	var f Floors
	if err := json.Unmarshal(raw, &f); err != nil {
		return Floors{}, false, fmt.Errorf("bench: %s: %w", path, err)
	}
	return f, true, nil
}

// Check says what of rep is under its floor, one line each; nothing when
// rep was scored by another judge than the floors were set with, which
// gated says.
func (f Floors) Check(rep report) (under []string, gated bool) {
	if rep.Judge != f.Judge {
		return nil, false
	}
	for _, m := range slices.Sorted(maps.Keys(f.Modes)) {
		floor, got := f.Modes[m], rep.Modes[m]
		if _, ran := rep.Modes[m]; !ran {
			continue
		}
		for _, c := range []struct {
			name       string
			got, floor float64
		}{
			{"must recall", got.MustRecall, floor.MustRecall},
			{"mean findings", got.MeanFindings, floor.MeanFindings},
			{"mean tool calls", got.MeanToolCalls, floor.MeanToolCalls},
		} {
			if c.got < c.floor {
				under = append(under, fmt.Sprintf("%s: %s %.3f is under its floor %.3f", m, c.name, c.got, c.floor))
			}
		}
	}
	return under, true
}

// FloorsFrom sets floors from runs scored by one judge: a mode's recall
// floor is the mean over the runs less floorSigmas standard deviations,
// at least 0, and its collapse floors are collapseShare of the means.
// Every run must have been scored by the same judge, and a mode is set
// from the runs that ran it.
func FloorsFrom(runs ...report) (Floors, error) {
	if len(runs) == 0 {
		return Floors{}, errors.New("bench: no runs to set floors from")
	}
	f := Floors{Judge: runs[0].Judge, Modes: map[string]ModeFloors{}}
	byMode := map[string][]modeTotals{}
	for _, r := range runs {
		if r.Judge != f.Judge {
			return Floors{}, fmt.Errorf("bench: runs scored by different judges: %q and %q", f.Judge, r.Judge)
		}
		for m, t := range r.Modes {
			byMode[m] = append(byMode[m], t)
		}
	}
	for m, ts := range byMode {
		var recall, findings, calls []float64
		for _, t := range ts {
			recall, findings, calls = append(recall, t.MustRecall), append(findings, t.MeanFindings), append(calls, t.MeanToolCalls)
		}
		mean, sigma := meanSigma(recall)
		f.Modes[m] = ModeFloors{
			MustRecall:    down3(max(0, mean-floorSigmas*sigma)),
			MeanFindings:  down3(collapseShare * meanOf(findings)),
			MeanToolCalls: down3(collapseShare * meanOf(calls)),
		}
	}
	return f, nil
}

func meanOf(xs []float64) float64 {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// meanSigma is the mean and the population standard deviation of xs.
func meanSigma(xs []float64) (mean, sigma float64) {
	mean = meanOf(xs)
	var sq float64
	for _, x := range xs {
		sq += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(sq / float64(len(xs)))
}

// down3 is x to the thousandth under it, so a floor written out is never
// over the measure it was set from, as rounding to the nearest could put
// 0.667 over a recall of two thirds.
func down3(x float64) float64 { return math.Floor(x*1000) / 1000 }
