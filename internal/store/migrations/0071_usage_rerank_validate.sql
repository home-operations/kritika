-- Checks the usage rows against the check that 0068_usage_rerank added. Its
-- own transaction, as validating takes a lock that lets the table be read
-- and written while it reads every row.
ALTER TABLE usage VALIDATE CONSTRAINT usage_role_check;
