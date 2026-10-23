package store

import "testing"

func TestGatewayGrantFor(t *testing.T) {
	g := GatewayGrant{Model: "p/a", Fallback: "p/b", Effort: "low", Reviewers: map[string]ReviewerGrant{"security": {Model: "p/big"}}}
	for _, name := range []string{"", "default"} {
		if got, ok := g.For(name); !ok || got.Model != "p/a" || got.Fallback != "p/b" || got.Effort != "low" {
			t.Errorf("For(%q) = %+v %v, want the grant's own models", name, got, ok)
		}
	}
	if got, ok := g.For("security"); !ok || got.Model != "p/big" {
		t.Errorf("For(security) = %+v %v", got, ok)
	}
	if _, ok := g.For("ghost"); ok {
		t.Error("a reviewer the grant does not name was granted")
	}
}
