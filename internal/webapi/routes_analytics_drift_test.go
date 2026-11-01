package webapi

import (
	"reflect"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// TestDependencyDrift: a row's ratings are counted, the key parts into the
// path and the line, and the last update's rating is the last of each.
func TestDependencyDrift(t *testing.T) {
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	got := dependencyDrift(store.DependencyDrift{
		Repository: "alpha/one", Key: "kubernetes/apps/o11y/kps/app/ocirepository.yaml tag: *", Path: "kubernetes/apps/o11y/kps/app/ocirepository.yaml",
		Risks: []string{"medium", "medium", "high"}, Reaches: []string{"shared", "central", "central"}, Steps: []string{"patch", "minor", "minor"}, LastAt: at,
	})
	want := DependencyDrift{
		Repository: "alpha/one", Path: "kubernetes/apps/o11y/kps/app/ocirepository.yaml", Dependency: "tag: *", Bumps: 3, Levels: 2,
		Risks: map[review.Risk]int{review.RiskMedium: 2, review.RiskHigh: 1}, Reaches: map[review.Reach]int{review.ReachShared: 1, review.ReachCentral: 2},
		LastAt: at, LastRisk: review.RiskHigh, LastReach: review.ReachCentral, LastStep: review.StepMinor,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencyDrift = %+v, want %+v", got, want)
	}
	if got := dependencyDrift(store.DependencyDrift{Repository: "r", Key: "my apps/deploy.yaml image: nginx:*", Path: "my apps/deploy.yaml"}); got.Path != "my apps/deploy.yaml" || got.Dependency != "image: nginx:*" {
		t.Fatalf("dependencyDrift of a path with a space = %+v", got)
	}
	if got := dependencyDrift(store.DependencyDrift{Repository: "r", Key: "k tag: *", Path: "k"}); got.Dependency != "tag: *" || got.Bumps != 0 || len(got.Risks) != 0 {
		t.Fatalf("dependencyDrift of an empty row = %+v", got)
	}
}
