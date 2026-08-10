package collector

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// sysUpTimeOID is sysUpTime.0 (RFC 1213, MIB-2 system group) — the one
// object every SNMP agent, regardless of vendor, implements. A device that
// answers it is reachable and running; the TimeTicks it returns are the
// device's own uptime. That makes it the correct probe for A.1's bounded
// scope (reachability + uptime): richer, vendor/MIB-specific OIDs
// (interfaces, CPU, memory) are A.1b's job, not this one's.
const sysUpTimeOID = ".1.3.6.1.2.1.1.3.0"

// defaultSNMPPort is the well-known SNMP agent port (RFC 1157), used when a
// check's Target omits one.
const defaultSNMPPort = 161

// snmpCheckResult is the outcome of one SNMP v2c reachability probe. Up
// reports whether the agent answered the GET within the bounded timeout —
// reachability, the same meaning httpCheckResult.Up carries for "http".
// UptimeTicks/UptimeSeconds are meaningful only when Up is true.
type snmpCheckResult struct {
	Up            bool
	UptimeTicks   uint64
	UptimeSeconds float64
}

// splitSNMPTarget parses a collector_check Target of "host" or "host:port"
// into gosnmp's separate Target/Port fields, defaulting to defaultSNMPPort
// when no port is given.
//
// net.SplitHostPort fails with "missing port in address" for a bare
// IPv4/hostname AND for a bare IPv6 literal (which itself contains colons,
// e.g. "2001:db8::1") — both fall back to "the whole string is the host,
// port 161", which is exactly what a target with no port should mean. Any
// other SplitHostPort error (malformed brackets, garbage input) is
// surfaced: httpapi.validateSNMPTarget rejects those at creation time, so
// reaching one here means the row was written before this validation
// existed or through a path that bypassed it — treated the same as any
// other malformed target: no response was ever going to be attempted.
func splitSNMPTarget(raw string) (host string, port uint16, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, fmt.Errorf("snmp target must not be empty")
	}
	h, p, splitErr := net.SplitHostPort(raw)
	if splitErr != nil {
		return raw, defaultSNMPPort, nil
	}
	if h == "" {
		return "", 0, fmt.Errorf("snmp target %q has no host", raw)
	}
	n, convErr := strconv.ParseUint(p, 10, 16)
	if convErr != nil || n == 0 {
		return "", 0, fmt.Errorf("snmp target %q has an invalid port", raw)
	}
	return h, uint16(n), nil
}

// runSNMPCheck performs one bounded SNMP v2c GET of sysUpTime.0 against
// target through community. It NEVER returns an error: a timeout, a
// connection refusal (ICMP port-unreachable), a malformed target, or a
// response with no usable value are all exactly as reportable as "the
// device did not answer" — the same "failed/timed-out check records up=0,
// not an error that stalls the scheduler" rule runHTTPCheck follows.
//
// ctx carries the per-check timeout (context.WithTimeout, set by the
// caller). gosnmp.GoSNMP.Context is wired to it, and gosnmp's own dial and
// send/receive paths honour that context's deadline directly (see
// GoSNMP.netConnect/sendOneRequest in the gosnmp source) — the same
// bounded-regardless-of-target-behaviour guarantee ctx gives runHTTPCheck,
// so a device that never answers (a "black hole", not even an ICMP
// rejection) cannot outlast ctx either. Retries is 1: a single retransmit
// covers ordinary UDP packet loss without meaningfully changing the worst
// case, which ctx already bounds regardless of Retries.
//
// v2c only — this increment's bounded scope (A.1). v3 needs no shared
// community secret at all, so it does not carry the redaction burden this
// type does; it is a deliberate, flagged follow-up (ADR-NET-001), not
// solved here.
func runSNMPCheck(ctx context.Context, target, community string, timeout time.Duration) snmpCheckResult {
	host, port, err := splitSNMPTarget(target)
	if err != nil {
		return snmpCheckResult{Up: false}
	}

	g := &gosnmp.GoSNMP{
		Target:    host,
		Port:      port,
		Community: community,
		Version:   gosnmp.Version2c,
		Timeout:   timeout,
		Retries:   1,
		Context:   ctx,
	}
	if err := g.Connect(); err != nil {
		return snmpCheckResult{Up: false}
	}
	defer func() { _ = g.Close() }()

	result, err := g.Get([]string{sysUpTimeOID})
	if err != nil || result == nil || len(result.Variables) != 1 {
		return snmpCheckResult{Up: false}
	}

	v := result.Variables[0]
	switch v.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		// The agent answered but does not implement sysUpTime — treated as
		// "did not usefully answer", not a crash: reachability could not be
		// confirmed the way this check confirms it.
		return snmpCheckResult{Up: false}
	}

	ticks := gosnmp.ToBigInt(v.Value).Uint64()
	return snmpCheckResult{Up: true, UptimeTicks: ticks, UptimeSeconds: float64(ticks) / 100.0}
}
