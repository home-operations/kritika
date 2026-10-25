-- Findings and dismissals are recorded as p0 to p2 alone; nothing renames
-- a severity as it lands.
DROP TRIGGER findings_severity_p_level ON findings;
DROP TRIGGER dismissals_severity_p_level ON dismissals;
DROP FUNCTION kritika_severity_p_level();
