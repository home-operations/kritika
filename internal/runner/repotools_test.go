package runner

import (
	"slices"
	"strings"
	"testing"
)

func TestNewRepoMiseConfig(t *testing.T) {
	t.Run("no tools", func(t *testing.T) {
		r, err := newRepoMiseConfig("")
		if r != nil || err != nil {
			t.Fatalf("= %v, %v", r, err)
		}
	})
	t.Run("the config is kept as rendered, its options read", func(t *testing.T) {
		doc := "[tools]\nhelm = '4.3.0'\n\n[tools.'github:home-operations/flate']\nversion = '0.6.5'\nfilter_bins = ['flate', 'flate-x']\n" +
			"\n[tools.'http:tool']\nversion = '1.0.0'\nfilter_bins = 'tool'\n"
		r, err := newRepoMiseConfig(doc)
		if err != nil {
			t.Fatal(err)
		}
		if r.config != doc {
			t.Fatalf("config = %q", r.config)
		}
		for name, want := range map[string][]string{
			"helm": nil, "github:home-operations/flate": {"flate", "flate-x"}, "http:tool": {"tool"}, "absent": nil,
		} {
			if got := r.filterBins(name); !slices.Equal(got, want) {
				t.Errorf("filterBins(%s) = %v, want %v", name, got, want)
			}
		}
	})
	t.Run("not TOML", func(t *testing.T) {
		if _, err := newRepoMiseConfig("[tools"); err == nil || !strings.Contains(err.Error(), "repository tools") {
			t.Fatalf("err = %v", err)
		}
	})
}
