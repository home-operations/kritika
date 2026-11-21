//go:build integration

package store

import (
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// indexChunksLayout is index_chunks as the catalog describes it, by names and
// definitions rather than OIDs: every relation of its partition tree with its
// bound, row-level security and grants, every column, every index and the
// index it is a partition of, every constraint and the constraint it is a
// partition of, and the policies.
func indexChunksLayout(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.owner.Query(t.Context(), `WITH tree AS (SELECT relid FROM pg_partition_tree('index_chunks'))
		SELECT 'relation ' || c.relname || ' kind=' || c.relkind::text || ' parent=' || coalesce(p.relname, '') ||
			' bound=' || coalesce(pg_get_expr(c.relpartbound, c.oid), '') || ' rls=' || c.relrowsecurity ||
			' acl=' || coalesce(c.relacl::text, '')
		FROM tree JOIN pg_class c ON c.oid = tree.relid
		LEFT JOIN pg_inherits h ON h.inhrelid = c.oid LEFT JOIN pg_class p ON p.oid = h.inhparent
		UNION ALL
		SELECT 'column ' || c.relname || '.' || a.attnum || ' ' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod) ||
			' notnull=' || a.attnotnull || ' default=' || coalesce(pg_get_expr(d.adbin, d.adrelid), '')
		FROM tree JOIN pg_class c ON c.oid = tree.relid
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
		UNION ALL
		SELECT 'index ' || pg_get_indexdef(x.indexrelid) || ' parent=' || coalesce(p.relname, '')
		FROM tree JOIN pg_index x ON x.indrelid = tree.relid
		LEFT JOIN pg_inherits h ON h.inhrelid = x.indexrelid LEFT JOIN pg_class p ON p.oid = h.inhparent
		UNION ALL
		SELECT 'constraint ' || c.relname || '.' || k.conname || ' ' || pg_get_constraintdef(k.oid) ||
			' parent=' || coalesce(pk.conname, '')
		FROM tree JOIN pg_class c ON c.oid = tree.relid JOIN pg_constraint k ON k.conrelid = c.oid
		LEFT JOIN pg_constraint pk ON pk.oid = k.conparentid
		UNION ALL
		SELECT 'policy ' || tablename || '.' || policyname || ' ' || roles::text || ' ' || cmd || ' ' ||
			coalesce(qual, '') || ' ' || coalesce(with_check, '')
		FROM pg_policies WHERE tablename LIKE 'index\_chunks%'
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return layout
}

// TestLegacyConversionEndsInFreshLayout: once the retention sweep has
// partitioned an index_chunks of a build before partitioning, drained it and
// dropped its legacy partition, the catalog describes the table exactly as it
// describes one created partitioned: the same partitions and bounds, columns,
// indexes, constraints, policies and grants. That holds whether or not the
// plain table had content hashes.
func TestLegacyConversionEndsInFreshLayout(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-layout"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-layout", "index-layout/one")
	account := accountID(t, s, "index-layout")
	repo := configfile.RepositoryID(account, "index-layout/one")
	dropIndexChunks(t, s)
	reset := func() {
		t.Helper()
		if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL;
			DROP TABLE IF EXISTS index_chunks; DROP TABLE IF EXISTS index_chunks_legacy; DELETE FROM index_schema`); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		hash bool
	}{
		{name: "with content hashes", hash: true},
		{name: "without content hashes", hash: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
				t.Fatal(err)
			}
			fresh := indexChunksLayout(t, s)
			if len(fresh) == 0 {
				t.Fatal("a fresh index_chunks has no layout")
			}

			reset()
			createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, tc.hash)
			// The chunk is written by hand, as the table may lack the content
			// hash that InsertIndexChunks writes.
			var run string
			if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
				VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed') RETURNING id`, account, repo).Scan(&run); err != nil {
				t.Fatal(err)
			}
			if _, err := s.owner.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
				VALUES ($1, $2, $3, 'a.go', 1, 2, 'package main', '[1,0,0,0,0,0,0,0]')`, account, repo, run); err != nil {
				t.Fatal(err)
			}
			if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = $2 WHERE id = $1`, repo, run); err != nil {
				t.Fatal(err)
			}
			partitionLegacy(t, s)
			drainLegacy(t, s)
			if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || !dropped {
				t.Fatalf("DropLegacyIndexChunks after the drain = %v, %v", dropped, err)
			}
			converted := indexChunksLayout(t, s)
			if !slices.Equal(fresh, converted) {
				var onlyFresh, onlyConverted []string
				for _, l := range fresh {
					if !slices.Contains(converted, l) {
						onlyFresh = append(onlyFresh, l)
					}
				}
				for _, l := range converted {
					if !slices.Contains(fresh, l) {
						onlyConverted = append(onlyConverted, l)
					}
				}
				t.Fatalf("the converted index_chunks is laid out unlike a fresh one\nonly fresh:\n%s\nonly converted:\n%s",
					strings.Join(onlyFresh, "\n"), strings.Join(onlyConverted, "\n"))
			}
			if got := similarPaths(t, s, account, run); strings.Join(got, ",") != "a.go" {
				t.Fatalf("search of the converted generation found %v, want a.go", got)
			}
		})
	}
}
