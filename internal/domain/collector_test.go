package domain

import (
	"strings"
	"testing"
)

func TestNewCollectorCheck_BuildsAnEnabledCheck(t *testing.T) {
	c, err := NewCollectorCheck(" tn-1 ", " asset-1 ", " http ", " https://example.test/health ", 60, " checkout_api ")
	if err != nil {
		t.Fatalf("NewCollectorCheck: %v", err)
	}
	if c.TenantID != "tn-1" || c.AssetID != "asset-1" || c.Type != "http" ||
		c.Target != "https://example.test/health" || c.MetricPrefix != "checkout_api" {
		t.Errorf("identifiers not trimmed: %+v", c)
	}
	if !c.Enabled {
		t.Error("a new check must be enabled by default")
	}
	if c.CheckID == "" {
		t.Error("check_id must be minted")
	}
}

func TestNewCollectorCheck_RequiresTenantAssetTypeTargetPrefix(t *testing.T) {
	for _, c := range []struct {
		name, tenant, asset, checkType, target, prefix string
	}{
		{"no tenant", "", "asset-1", "http", "https://x", "p"},
		{"no asset", "tn-1", "", "http", "https://x", "p"},
		{"no type", "tn-1", "asset-1", "", "https://x", "p"},
		{"no target", "tn-1", "asset-1", "http", "", "p"},
		{"no prefix", "tn-1", "asset-1", "http", "https://x", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewCollectorCheck(c.tenant, c.asset, c.checkType, c.target, 60, c.prefix); err == nil {
				t.Error("an incomplete collector check was constructed")
			}
		})
	}
}

func TestNewCollectorCheck_RejectsIntervalOutOfBounds(t *testing.T) {
	if _, err := NewCollectorCheck("tn-1", "asset-1", "http", "https://x", MinCollectorCheckIntervalSeconds-1, "p"); err == nil {
		t.Error("an interval below the floor was accepted")
	}
	if _, err := NewCollectorCheck("tn-1", "asset-1", "http", "https://x", MaxCollectorCheckIntervalSeconds+1, "p"); err == nil {
		t.Error("an interval above the ceiling was accepted")
	}
	if _, err := NewCollectorCheck("tn-1", "asset-1", "http", "https://x", MinCollectorCheckIntervalSeconds, "p"); err != nil {
		t.Errorf("the floor itself was rejected: %v", err)
	}
	if _, err := NewCollectorCheck("tn-1", "asset-1", "http", "https://x", MaxCollectorCheckIntervalSeconds, "p"); err != nil {
		t.Errorf("the ceiling itself was rejected: %v", err)
	}
}

func TestNewCollectorCheck_RejectsAnOverlongOrMalformedType(t *testing.T) {
	if _, err := NewCollectorCheck("tn-1", "asset-1", strings.Repeat("a", MaxCollectorCheckTypeLength+1), "https://x", 60, "p"); err == nil {
		t.Error("an over-long type was accepted")
	}
	if _, err := NewCollectorCheck("tn-1", "asset-1", "HTTP", "https://x", 60, "p"); err == nil {
		t.Error("an upper-case type was accepted")
	}
}

func TestNewCollectorCheck_RejectsAnOverlongOrMalformedMetricPrefix(t *testing.T) {
	if _, err := NewCollectorCheck("tn-1", "asset-1", "http", "https://x", 60, strings.Repeat("a", MaxCollectorMetricPrefixLength+1)); err == nil {
		t.Error("an over-long metric_prefix was accepted")
	}
	if _, err := NewCollectorCheck("tn-1", "asset-1", "http", "https://x", 60, "Checkout-API"); err == nil {
		t.Error("a metric_prefix with hyphens/upper-case was accepted")
	}
}

func TestNewCollectorCheck_RejectsAnOverlongTarget(t *testing.T) {
	if _, err := NewCollectorCheck("tn-1", "asset-1", "http", "https://x/"+strings.Repeat("a", MaxCollectorCheckTargetLength), 60, "p"); err == nil {
		t.Error("an over-long target was accepted")
	}
}

func TestCollectorCheckStatus_Valid(t *testing.T) {
	for _, s := range []CollectorCheckStatus{CollectorCheckStatusOK, CollectorCheckStatusDown, CollectorCheckStatusUnsupported} {
		if !s.Valid() {
			t.Errorf("%q must be valid", s)
		}
	}
	if CollectorCheckStatus("bogus").Valid() {
		t.Error("an unknown status was accepted")
	}
}
