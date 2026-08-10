# ADR-NET-001 — SNMP device monitoring is a collector-check type feeding telemetry, not a new Device stack

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-08 |
| **Decider** | Acting CTO |
| **Related** | ADR-TELEMETRY-001 (the telemetry pipeline this feeds), E2.2a (the collector + HTTP-check pattern this extends), ADR-SECURITY-001/003 (SSRF egress — why it doesn't apply here), `internal/collector/snmp_check.go`, `internal/domain/collector.go`, migration `20260915000001_collector_check_snmp_community.sql`, `docs/PLATFORM-BUILD-PLAN.md` E-NOC-NET A.1. **First story of the network/device layer (Phase A) — the ATECH-parity direction.** |

## Context

OneOps has a strong ops spine (detect → correlate → incident → page → escalate,
multi-tenant + audited) but only HTTP active checks — it can't monitor network
devices. ATECH (the founder's proven prior NOC) monitors devices via SNMP;
that's the defining NOC gap. The collector was built anticipating this: its
domain comment literally reserves the shape "a host and community string for a
future SNMP" check, and the scheduler already turns any check's result into
telemetry samples that flow into alerting/incidents.

## Decision

**SNMP monitoring is a new `snmp` `collector_check` type, not a new Device
subsystem.** An SNMP check is a `collector_check` on an asset; its result is
`telemetry_sample` rows like any other check's. No `Device` noun is reified
(reduced-concept) — the CMDB `asset` is the device; the collector is the sensor;
telemetry is the data; the existing alert→incident→escalate engine is the
reaction, untouched.

A.1 is bounded to **reachability**: the scheduler's `runSNMP` does a v2c GET of
`sysUpTime.0` (OID `1.3.6.1.2.1.1.3.0`) via `github.com/gosnmp/gosnmp`, emitting
`<metric_prefix>_reachable` (1/0) and `<metric_prefix>_uptime_seconds`
(snake_case, matching every other metric in the corpus). A down/timeout/
malformed target yields `reachable=0` + `last_status=down` and NEVER an error
that stalls the scheduler (mirrors `runHTTPCheck`; gosnmp's dial+send honor the
caller's `ctx` timeout). So an operator's existing alert rule on
`<prefix>_reachable` fires on device-down through the normal loop (proven
end-to-end: the real scheduler writes the samples, read back through the
tenant-scoped telemetry store).

### The community string is a write-only, redacted credential

`snmp_community` is added as a nullable column on the (already tenant-owned,
RLS-scoped) `collector_check`. It is **write-only**: accepted on create/patch,
and the read DTO (`collectorCheckDTO`) has NO community field of any kind — only
a `has_snmp_community` bool. The raw value never appears in a response, a log
line, or an error (grep-verified; gosnmp's logger left at its no-op zero value).
Required + length-bounded when `type=snmp`, forbidden otherwise.

### Egress: the HTTP-SSRF guard does not apply

SNMP is UDP:161 to an operator-configured device IP (internal/private is the
norm for infra monitoring). The `safehttp` SSRF guard is HTTP-specific and a
UDP:161 probe cannot reach an HTTP metadata/service endpoint, so that guard's
class genuinely doesn't apply — confirmed against `internal/arch`'s
dependency + outbound-HTTP guards, which sweep `net/http` clients (gosnmp uses
`net.Dial`), and were satisfied without weakening. The target is validated as a
well-formed host[:port].

## Consequences

**Guaranteed.** OneOps now actively monitors device reachability via SNMP; a
down device produces telemetry that lights up the existing NOC loop — the first
real network-layer capability, on the hardened multi-tenant/audited base ATECH
lacks. Additive migration; alerting/incident/escalation code untouched.

**Deferred (flagged in code).** Richer OIDs — IF-MIB interface status/bandwidth,
CPU/mem (A.1b); network discovery (A.2); **SNMP v3** (auth/priv); and **at-rest
encryption of the community** (no secret store exists in the platform yet — RLS
scoping + response/log redaction is the whole of the current protection, and v2c
communities are weak-by-protocol regardless). These are the honest next steps,
not gaps hidden.

## Enforcement

- `internal/collector/snmp_check_test.go` (mock UDP SNMP agent: reachable→up+
  uptime, dead→down, no stall) + `internal/store/postgres/collector_scheduler_snmp_integration_test.go`
  (real scheduler + PG → reachability telemetry lands; community persists) +
  the httpapi redaction tests (community never in the JSON body).
- The read DTO must never gain a community field; the community must stay
  write-only + RLS-scoped; SNMP failures must stay non-stalling.
- `internal/arch` (deps + SSRF-client guards) must stay green with the gosnmp
  dependency; it must not be weakened to admit it.
