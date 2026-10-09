-- Severities are named p0, p1 and p2, as the badges on the comments
-- already read; findings and dismissals recorded under the earlier names
-- follow, so an upgrade keeps them.
ALTER TABLE findings DROP CONSTRAINT findings_severity_check;
UPDATE findings SET severity = CASE severity
    WHEN 'blocking' THEN 'p0' WHEN 'important' THEN 'p1' WHEN 'nit' THEN 'p2' ELSE severity END
    WHERE severity IN ('blocking', 'important', 'nit');
ALTER TABLE findings ADD CONSTRAINT findings_severity_check CHECK (severity IN ('p0', 'p1', 'p2'));
UPDATE dismissals SET severity = CASE severity
    WHEN 'blocking' THEN 'p0' WHEN 'important' THEN 'p1' WHEN 'nit' THEN 'p2' ELSE severity END
    WHERE severity IN ('blocking', 'important', 'nit');

-- A replica of an earlier release keeps working jobs through a rolling
-- update, after the new leader has applied this, and records the earlier
-- names: they are renamed as they land, rather than failing the check once
-- the review's comments are already posted.
CREATE FUNCTION kritika_severity_p_level() RETURNS trigger AS $$
BEGIN
    NEW.severity := CASE NEW.severity
        WHEN 'blocking' THEN 'p0' WHEN 'important' THEN 'p1' WHEN 'nit' THEN 'p2' ELSE NEW.severity END;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER findings_severity_p_level
    BEFORE INSERT OR UPDATE OF severity ON findings
    FOR EACH ROW
    EXECUTE FUNCTION kritika_severity_p_level();

CREATE TRIGGER dismissals_severity_p_level
    BEFORE INSERT OR UPDATE OF severity ON dismissals
    FOR EACH ROW
    EXECUTE FUNCTION kritika_severity_p_level();
