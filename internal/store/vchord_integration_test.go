//go:build integration

package store

import (
	"context"
	"testing"
)

// TestVectorChordVersion: the suite runs on VectorChord 1.3.0 or later, the
// release that database.md deploys, with its fix for results that go missing
// after VACUUM. A vchordrq index built on a table that holds rows records
// them, as 1.3.0 does and 1.1.1 did not, so the planner does not take a
// partition whose index was just built for an empty one.
func TestVectorChordVersion(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	var version string
	var supported bool
	if err := s.owner.QueryRow(ctx, `SELECT extversion, string_to_array(extversion, '.')::int[] >= '{1,3,0}'
		FROM pg_extension WHERE extname = 'vchord'`).Scan(&version, &supported); err != nil {
		t.Fatal(err)
	}
	if !supported {
		t.Fatalf("vchord %s, want 1.3.0 or later", version)
	}

	tx, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var tuples float64
	for _, stmt := range []string{
		`CREATE TABLE vchord_reltuples (embedding halfvec(3) NOT NULL)`,
		`INSERT INTO vchord_reltuples SELECT ARRAY[1, i, 0]::real[]::halfvec FROM generate_series(1, 100) AS i`,
		`CREATE INDEX ON vchord_reltuples USING vchordrq (embedding halfvec_cosine_ops)`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := tx.QueryRow(ctx, `SELECT reltuples FROM pg_class WHERE oid = 'vchord_reltuples'::regclass`).Scan(&tuples); err != nil {
		t.Fatal(err)
	}
	if tuples != 100 {
		t.Fatalf("vchord %s recorded %v rows for a table of 100 as it built its index, want 100", version, tuples)
	}
}
