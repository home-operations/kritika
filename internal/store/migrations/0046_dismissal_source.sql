-- What dismissed a finding: a maintainer's "@<bot> dismiss <reason>"
-- reply, a maintainer resolving its thread, or kritika conceding it in a
-- reply. Its default is a constant, so adding it rewrites no row; the
-- update then tells the existing rows apart by the reasons the two other
-- paths write.
ALTER TABLE dismissals ADD COLUMN source text NOT NULL DEFAULT 'command'
    CHECK (source IN ('command', 'resolved', 'conceded'));
UPDATE dismissals SET source = 'resolved' WHERE reason LIKE 'thread resolved by %';
UPDATE dismissals SET source = 'conceded' WHERE reason LIKE 'kritika conceded: %';
