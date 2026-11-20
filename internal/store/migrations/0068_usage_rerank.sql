-- A reranker's call, which reorders a similar-code search's candidates, is
-- charged and recorded as its own when its provider reports what it spent.
ALTER TABLE usage DROP CONSTRAINT usage_role_check;
ALTER TABLE usage ADD CONSTRAINT usage_role_check
    CHECK (role IN ('review', 'embedding', 'followup', 'confidence', 'rerank'));
