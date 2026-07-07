-- RIZ-59: per-account brute-force login lockout, per
-- documentation/security.md §API hardening ("brute-force lockout on
-- login ... independent of per-IP rate limiting").
--
-- Columns (not a companion table) are chosen because this is 1:1,
-- always-present state on the account row itself (mirrors password_hash /
-- apple_user_id already living directly on users rather than in a side
-- table), and because the lockout gate in Service.Login needs this state
-- on every login attempt in the same row it already fetches via
-- GetUserByEmail — a companion table would require an extra join/lookup
-- on every single login for state that is always exactly one row per user
-- and never queried independently of the user row.
ALTER TABLE users
    ADD COLUMN failed_login_attempts int NOT NULL DEFAULT 0,
    ADD COLUMN lockout_count        int NOT NULL DEFAULT 0,
    ADD COLUMN locked_until         timestamptz;
