//go:build integration

package store

import (
	"context"
	"testing"
)

// TestSchemaReadyWaitsForGrants: the last migration commits before grant
// gives the application role the tables it made, and a replica that starts
// working jobs between the two fails on them. So a table privilege that
// grant gives and the role lacks keeps the schema unready until grant runs.
func TestSchemaReadyWaitsForGrants(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	for _, tt := range []struct{ privilege, table string }{
		{"INSERT", "similar_exclusions"},
		{"SELECT", "similar_exclusions"},
		{"DELETE", "index_postings"},
		{"UPDATE", "index_runs"},
	} {
		t.Run(tt.privilege+" on "+tt.table, func(t *testing.T) {
			t.Cleanup(func() {
				if err := s.grant(context.Background(), "kritika_app"); err != nil {
					t.Error(err)
				}
			})
			if _, err := s.owner.Exec(ctx, `REVOKE `+tt.privilege+` ON `+tt.table+` FROM kritika_app`); err != nil {
				t.Fatal(err)
			}
			if ready, err := s.SchemaReady(ctx); err != nil || ready {
				t.Fatalf("SchemaReady without %s on %s = %v, %v; want false", tt.privilege, tt.table, ready, err)
			}
			if err := s.grant(ctx, "kritika_app"); err != nil {
				t.Fatal(err)
			}
			if ready, err := s.SchemaReady(ctx); err != nil || !ready {
				t.Fatalf("SchemaReady after grant = %v, %v; want true", ready, err)
			}
		})
	}
}
