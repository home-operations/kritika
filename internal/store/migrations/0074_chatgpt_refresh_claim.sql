-- refreshing_until is when a leader's claim on refreshing a session's token
-- set ends. ChatGPT rotates the refresh token on every refresh, so a new
-- leader that refreshed the same set while the last one's refresh runs on
-- would be refused, and the session signed out.
ALTER TABLE chatgpt_sessions ADD COLUMN refreshing_until timestamptz;
