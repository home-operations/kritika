package worker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
)

// TestStartedAt: a checkpoint carries the review's first pickup, which a
// resumed stage times the review from; one from before it was carried, or
// one that cannot be read, leaves the stage's own.
func TestStartedAt(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	fallback := started.Add(time.Hour)
	carried, err := json.Marshal(publicationSnapshot{Version: 2, Started: started})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		payload string
		want    time.Time
	}{
		{name: "carried", payload: string(carried), want: started},
		{name: "not carried", payload: `{"Version":1}`, want: fallback},
		{name: "unreadable", payload: `{`, want: fallback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := startedAt(json.RawMessage(tt.payload), fallback); !got.Equal(tt.want) {
				t.Fatalf("startedAt = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestRestoreDashboard: a resumed publication takes comments.dashboard from
// the configuration it resumes under, for its repository, whatever it was
// saved with.
func TestRestoreDashboard(t *testing.T) {
	t.Parallel()
	off := configfile.Overrides{Comments: configfile.CommentsSpec{Dashboard: new(false)}}
	tests := []struct {
		name    string
		saved   bool
		file    *configfile.File
		entries []configfile.Repository
		want    bool
	}{
		{name: "saved on, now off", saved: true, file: &configfile.File{Defaults: configfile.Defaults{Overrides: off}}},
		{name: "saved off, now on", file: &configfile.File{}, want: true},
		{name: "now off for its repository", saved: true, file: &configfile.File{},
			entries: []configfile.Repository{{Name: "x", Overrides: off}}},
		{name: "now off for another repository", saved: true, file: &configfile.File{},
			entries: []configfile.Repository{{Name: "y", Overrides: off}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(publicationSnapshot{Version: 1, Review: configfile.Review{InlineComments: true, Dashboard: tt.saved}})
			if err != nil {
				t.Fatal(err)
			}
			p := &publishPhase{
				file: tt.file, account: &configfile.Account{Forge: configfile.ForgeGitHub, Name: "acme", Repositories: tt.entries},
				pr: &pullRequest{repository: "acme/x"},
			}
			if _, err := p.restore(payload); err != nil {
				t.Fatal(err)
			}
			if got := p.settings.Review; got.Dashboard != tt.want || !got.InlineComments {
				t.Fatalf("review = %+v, want dashboard %v and the rest as saved", got, tt.want)
			}
		})
	}
}
