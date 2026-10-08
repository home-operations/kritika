-- How each part of a split review's agent ended, in part order: the files
-- it reviewed, why it stopped, its steps and, once it submitted, the
-- summary it wrote. Empty for a review that was not split.
ALTER TABLE agent_runs ADD COLUMN parts jsonb NOT NULL DEFAULT '[]'::jsonb;

-- A split review some of whose parts ended before they submitted: it posts
-- what the others found, but its unreviewed files make it no review for a
-- later one to build on, to skip an unchanged rebase by, or to carry a
-- score or a risk from.
ALTER TABLE reviews ADD COLUMN partial boolean NOT NULL DEFAULT false;
