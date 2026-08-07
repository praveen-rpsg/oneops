-- Reverses 20260912000001_compliance_control.
--
-- control_evidence carries a foreign key to compliance_control (by design —
-- see the forward migration's comment on why this table differs from
-- asset_change_history), so it must be dropped before compliance_control
-- itself.
DROP TABLE IF EXISTS control_evidence;
DROP TABLE IF EXISTS compliance_control;
