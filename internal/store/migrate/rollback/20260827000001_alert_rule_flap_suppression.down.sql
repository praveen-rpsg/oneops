-- Reverses 20260827000001_alert_rule_flap_suppression.

ALTER TABLE alert_rule DROP CONSTRAINT IF EXISTS ck_alert_rule_pending_state_since_paired;
ALTER TABLE alert_rule DROP COLUMN IF EXISTS pending_since;

ALTER TABLE alert_rule DROP CONSTRAINT IF EXISTS ck_alert_rule_pending_state;
ALTER TABLE alert_rule DROP COLUMN IF EXISTS pending_state;

ALTER TABLE alert_rule DROP CONSTRAINT IF EXISTS ck_alert_rule_flap_dwell_seconds;
ALTER TABLE alert_rule DROP COLUMN IF EXISTS flap_dwell_seconds;
