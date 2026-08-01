package domain

import (
	"strings"
	"testing"
)

func TestNewAsset_BuildsAnActiveAsset(t *testing.T) {
	a, err := NewAsset(" tn-1 ", " server ", " db-primary-01 ", map[string]any{"region": "us-east-1"})
	if err != nil {
		t.Fatalf("NewAsset: %v", err)
	}
	if a.TenantID != "tn-1" || a.Type != "server" || a.Name != "db-primary-01" {
		t.Errorf("identifiers/name not trimmed: %+v", a)
	}
	if a.Status != AssetActive {
		t.Errorf("status %q, want active", a.Status)
	}
	if !a.Active() {
		t.Error("a new asset must be active")
	}
	if a.AssetID == "" {
		t.Error("asset_id must be minted")
	}
	if a.Attributes["region"] != "us-east-1" {
		t.Errorf("attributes not carried through: %+v", a.Attributes)
	}
}

// A nil attributes map is stored as an empty object, never as a nil the
// caller has to guard against.
func TestNewAsset_NilAttributesBecomesEmptyMap(t *testing.T) {
	a, err := NewAsset("tn-1", "server", "host-1", nil)
	if err != nil {
		t.Fatalf("NewAsset: %v", err)
	}
	if a.Attributes == nil {
		t.Error("Attributes must not be nil")
	}
	if len(a.Attributes) != 0 {
		t.Errorf("Attributes = %+v, want empty", a.Attributes)
	}
}

func TestNewAsset_RequiresTenantAndName(t *testing.T) {
	for _, c := range []struct{ name, tenant, assetType, assetName string }{
		{"no tenant", "", "server", "Host"},
		{"blank tenant", "   ", "server", "Host"},
		{"no type", "tn-1", "", "Host"},
		{"blank type", "tn-1", "   ", "Host"},
		{"no name", "tn-1", "server", ""},
		{"blank name", "tn-1", "server", "   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewAsset(c.tenant, c.assetType, c.assetName, nil); err == nil {
				t.Error("an incomplete asset was constructed")
			}
		})
	}
}

func TestNewAsset_RejectsAnOverlongName(t *testing.T) {
	if _, err := NewAsset("tn-1", "server", strings.Repeat("x", MaxAssetNameLength+1), nil); err == nil {
		t.Error("a name over the length bound was accepted")
	}
}

func TestNewAsset_RejectsAnOverlongType(t *testing.T) {
	if _, err := NewAsset("tn-1", strings.Repeat("a", MaxAssetTypeLength+1), "Host", nil); err == nil {
		t.Error("a type over the length bound was accepted")
	}
}

// Type is open (any identifier a caller supplies is a candidate) but
// validated (must be a lower-case snake_case identifier).
func TestAsset_TypeIsOpenButValidated(t *testing.T) {
	for _, valid := range []string{"server", "network_device", "kubernetes_cluster", "s3_bucket"} {
		if _, err := NewAsset("tn-1", valid, "Host", nil); err != nil {
			t.Errorf("type %q should be accepted (open set): %v", valid, err)
		}
	}
	for _, invalid := range []string{"Server", "network device", "server!", "1server", ""} {
		if _, err := NewAsset("tn-1", invalid, "Host", nil); err == nil {
			t.Errorf("type %q should be rejected (must be lower-case snake_case)", invalid)
		}
	}
}

func TestAssetStatus_Valid(t *testing.T) {
	for _, s := range []AssetStatus{AssetActive, AssetRetired} {
		if !s.Valid() {
			t.Errorf("%q must be valid", s)
		}
	}
	for _, s := range []AssetStatus{"", "ACTIVE", "deleted", "suspended"} {
		if AssetStatus(s).Valid() {
			t.Errorf("%q must not be valid", s)
		}
	}
}

func TestAsset_ValidateRejectsUndefinedStatus(t *testing.T) {
	a, err := NewAsset("tn-1", "server", "Host", nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Status = "decommissioned"
	if err := a.Validate(); err == nil {
		t.Error("an undefined status was accepted")
	}
	if a.Active() {
		t.Error("an asset with an undefined status must not report active")
	}
}

func TestNewAssetRelationship_BuildsARelationship(t *testing.T) {
	r, err := NewAssetRelationship(" tn-1 ", " asset-a ", " asset-b ", RelationshipDependsOn)
	if err != nil {
		t.Fatalf("NewAssetRelationship: %v", err)
	}
	if r.TenantID != "tn-1" || r.FromAssetID != "asset-a" || r.ToAssetID != "asset-b" {
		t.Errorf("identifiers not trimmed: %+v", r)
	}
	if r.Type != RelationshipDependsOn {
		t.Errorf("type = %q, want depends_on", r.Type)
	}
	if r.RelationshipID == "" {
		t.Error("relationship_id must be minted")
	}
}

// The graph must not admit a self-loop: an asset cannot depend on itself.
func TestNewAssetRelationship_RejectsSelfRelationship(t *testing.T) {
	if _, err := NewAssetRelationship("tn-1", "asset-a", "asset-a", RelationshipDependsOn); err == nil {
		t.Error("a self-relationship was accepted")
	}
}

func TestNewAssetRelationship_RequiresBothEndpoints(t *testing.T) {
	for _, c := range []struct{ name, from, to string }{
		{"no from", "", "asset-b"},
		{"no to", "asset-a", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewAssetRelationship("tn-1", c.from, c.to, RelationshipDependsOn); err == nil {
				t.Error("a relationship missing an endpoint was constructed")
			}
		})
	}
}

func TestRelationshipType_Valid(t *testing.T) {
	for _, ty := range []RelationshipType{
		RelationshipDependsOn, RelationshipRunsOn, RelationshipConnectedTo, RelationshipMemberOf,
	} {
		if !ty.Valid() {
			t.Errorf("%q must be valid", ty)
		}
	}
	for _, ty := range []RelationshipType{"", "depends", "hosts", "owns"} {
		if RelationshipType(ty).Valid() {
			t.Errorf("%q must not be valid — the relationship type set is closed", ty)
		}
	}
}

func TestAssetRelationship_ValidateRejectsUnknownType(t *testing.T) {
	r, err := NewAssetRelationship("tn-1", "asset-a", "asset-b", RelationshipDependsOn)
	if err != nil {
		t.Fatal(err)
	}
	r.Type = "hosts"
	if err := r.Validate(); err == nil {
		t.Error("an undefined relationship type was accepted")
	}
}
