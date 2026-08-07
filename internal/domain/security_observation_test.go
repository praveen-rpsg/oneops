package domain

import (
	"strings"
	"testing"
	"time"
)

func TestNewSecurityObservation_BuildsATrimmedFact(t *testing.T) {
	ts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	o, err := NewSecurityObservation(" tn-1 ", " asset-1 ", " port_scan ", " wazuh ",
		ObservationSeverityHigh, ts, map[string]string{"actor": "203.0.113.5"})
	if err != nil {
		t.Fatalf("NewSecurityObservation: %v", err)
	}
	if o.TenantID != "tn-1" || o.AssetID != "asset-1" || o.ObservationType != "port_scan" || o.Source != "wazuh" {
		t.Errorf("identifiers not trimmed: %+v", o)
	}
	if o.Severity != ObservationSeverityHigh {
		t.Errorf("severity = %q, want %q", o.Severity, ObservationSeverityHigh)
	}
	if !o.ObservedAt.Equal(ts) {
		t.Errorf("observed_at = %v, want %v", o.ObservedAt, ts)
	}
	if o.Attributes["actor"] != "203.0.113.5" {
		t.Errorf("attributes did not round-trip: %+v", o.Attributes)
	}
}

func TestNewSecurityObservation_NilAttributesBecomeEmpty(t *testing.T) {
	o, err := NewSecurityObservation("tn-1", "asset-1", "port_scan", "wazuh",
		ObservationSeverityLow, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("NewSecurityObservation: %v", err)
	}
	if o.Attributes == nil {
		t.Error("nil attributes must become an empty map, not stay nil")
	}
}

func TestNewSecurityObservation_RequiresTenantAssetTypeSourceSeverityTimestamp(t *testing.T) {
	ts := time.Now().UTC()
	for _, c := range []struct {
		name                           string
		tenant, asset, obsType, source string
		severity                       ObservationSeverity
		observedAt                     time.Time
	}{
		{"no tenant", "", "asset-1", "port_scan", "wazuh", ObservationSeverityLow, ts},
		{"no asset", "tn-1", "", "port_scan", "wazuh", ObservationSeverityLow, ts},
		{"no type", "tn-1", "asset-1", "", "wazuh", ObservationSeverityLow, ts},
		{"no source", "tn-1", "asset-1", "port_scan", "", ObservationSeverityLow, ts},
		{"bad severity", "tn-1", "asset-1", "port_scan", "wazuh", ObservationSeverity("bogus"), ts},
		{"zero timestamp", "tn-1", "asset-1", "port_scan", "wazuh", ObservationSeverityLow, time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewSecurityObservation(c.tenant, c.asset, c.obsType, c.source, c.severity, c.observedAt, nil); err == nil {
				t.Error("an incomplete security observation was constructed")
			}
		})
	}
}

func TestNewSecurityObservation_RejectsAnOverlongOrMalformedType(t *testing.T) {
	ts := time.Now().UTC()
	if _, err := NewSecurityObservation("tn-1", "asset-1", strings.Repeat("a", MaxObservationTypeLength+1),
		"wazuh", ObservationSeverityLow, ts, nil); err == nil {
		t.Error("an over-long observation_type was accepted")
	}
	if _, err := NewSecurityObservation("tn-1", "asset-1", "Port-Scan", "wazuh", ObservationSeverityLow, ts, nil); err == nil {
		t.Error("a hyphenated/upper-case observation_type was accepted")
	}
}

func TestNewSecurityObservation_RejectsAnOverlongSource(t *testing.T) {
	ts := time.Now().UTC()
	if _, err := NewSecurityObservation("tn-1", "asset-1", "port_scan",
		strings.Repeat("a", MaxObservationSourceLength+1), ObservationSeverityLow, ts, nil); err == nil {
		t.Error("an over-long source was accepted")
	}
}

func TestNewSecurityObservation_RejectsTooManyAttributes(t *testing.T) {
	attrs := map[string]string{}
	for i := 0; i < MaxObservationAttributeCount+1; i++ {
		attrs[strings.Repeat("k", 1)+string(rune('a'+i))] = "v"
	}
	if _, err := NewSecurityObservation("tn-1", "asset-1", "port_scan", "wazuh",
		ObservationSeverityLow, time.Now().UTC(), attrs); err == nil {
		t.Error("an attribute set over the bound was accepted")
	}
}

func TestNewSecurityObservation_RejectsAnOverlongAttribute(t *testing.T) {
	ts := time.Now().UTC()
	if _, err := NewSecurityObservation("tn-1", "asset-1", "port_scan", "wazuh", ObservationSeverityLow, ts,
		map[string]string{strings.Repeat("k", MaxObservationAttributeKeyLength+1): "v"}); err == nil {
		t.Error("an over-long attribute key was accepted")
	}
	if _, err := NewSecurityObservation("tn-1", "asset-1", "port_scan", "wazuh", ObservationSeverityLow, ts,
		map[string]string{"k": strings.Repeat("v", MaxObservationAttributeValueLength+1)}); err == nil {
		t.Error("an over-long attribute value was accepted")
	}
	if _, err := NewSecurityObservation("tn-1", "asset-1", "port_scan", "wazuh", ObservationSeverityLow, ts,
		map[string]string{" ": "v"}); err == nil {
		t.Error("a blank attribute key was accepted")
	}
}

func TestObservationSeverity_Valid(t *testing.T) {
	for _, s := range []ObservationSeverity{
		ObservationSeverityInfo, ObservationSeverityLow, ObservationSeverityMedium,
		ObservationSeverityHigh, ObservationSeverityCritical,
	} {
		if !s.Valid() {
			t.Errorf("%q must be valid", s)
		}
	}
	if ObservationSeverity("bogus").Valid() {
		t.Error("an unknown severity was accepted")
	}
}
