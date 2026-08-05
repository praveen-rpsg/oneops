-- Reverses 20260831000001_alert_rule_symptom_class.

ALTER TABLE alert_rule DROP CONSTRAINT IF EXISTS ck_alert_rule_symptom_class;
ALTER TABLE alert_rule DROP COLUMN IF EXISTS symptom_class;
