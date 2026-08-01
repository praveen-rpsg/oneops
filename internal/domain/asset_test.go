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

// NewAsset alone (before any classification is applied) already defaults to
// the "unknown" tier on both axes, exactly as the migration column default
// does — a caller that only cares about name/type never has to think about
// environment/criticality at all.
func TestNewAsset_DefaultsClassificationToUnknown(t *testing.T) {
	a, err := NewAsset("tn-1", "server", "Host", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Environment != AssetEnvironmentUnknown {
		t.Errorf("environment = %q, want unknown", a.Environment)
	}
	if a.Criticality != AssetCriticalityUnknown {
		t.Errorf("criticality = %q, want unknown", a.Criticality)
	}
	if a.OwnerTeamID != nil || a.OwnerUserID != nil {
		t.Errorf("a fresh asset must be unowned: %+v", a)
	}
}

func TestAssetEnvironment_Valid(t *testing.T) {
	for _, e := range []AssetEnvironment{AssetEnvironmentProduction, AssetEnvironmentStaging, AssetEnvironmentDevelopment, AssetEnvironmentUnknown} {
		if !e.Valid() {
			t.Errorf("%q must be valid", e)
		}
	}
	for _, e := range []AssetEnvironment{"", "PRODUCTION", "prod", "test"} {
		if e.Valid() {
			t.Errorf("%q must not be valid — the environment set is closed", e)
		}
	}
}

func TestAssetCriticality_Valid(t *testing.T) {
	for _, c := range []AssetCriticality{AssetCriticalityCritical, AssetCriticalityHigh, AssetCriticalityMedium, AssetCriticalityLow, AssetCriticalityUnknown} {
		if !c.Valid() {
			t.Errorf("%q must be valid", c)
		}
	}
	for _, c := range []AssetCriticality{"", "CRITICAL", "urgent", "p1"} {
		if c.Valid() {
			t.Errorf("%q must not be valid — the criticality set is closed", c)
		}
	}
}

func TestAsset_ApplyClassification_DefaultsBlankToUnknown(t *testing.T) {
	a, err := NewAsset("tn-1", "service", "checkout", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyClassification("", "", nil, nil); err != nil {
		t.Fatalf("ApplyClassification: %v", err)
	}
	if a.Environment != AssetEnvironmentUnknown || a.Criticality != AssetCriticalityUnknown {
		t.Errorf("blank classification did not default to unknown: %+v", a)
	}
}

func TestAsset_ApplyClassification_SetsValues(t *testing.T) {
	a, err := NewAsset("tn-1", "service", "checkout", nil)
	if err != nil {
		t.Fatal(err)
	}
	teamID, userID := "team-1", "user-1"
	if err := a.ApplyClassification("production", "critical", &teamID, &userID); err != nil {
		t.Fatalf("ApplyClassification: %v", err)
	}
	if a.Environment != AssetEnvironmentProduction || a.Criticality != AssetCriticalityCritical {
		t.Errorf("classification not applied: %+v", a)
	}
	if a.OwnerTeamID == nil || *a.OwnerTeamID != "team-1" {
		t.Errorf("owner_team_id not applied: %+v", a.OwnerTeamID)
	}
	if a.OwnerUserID == nil || *a.OwnerUserID != "user-1" {
		t.Errorf("owner_user_id not applied: %+v", a.OwnerUserID)
	}
}

func TestAsset_ApplyClassification_RejectsUndefinedEnumValues(t *testing.T) {
	a, err := NewAsset("tn-1", "service", "checkout", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyClassification("prod", "critical", nil, nil); err == nil {
		t.Error("an undefined environment was accepted")
	}
	if err := a.ApplyClassification("production", "urgent", nil, nil); err == nil {
		t.Error("an undefined criticality was accepted")
	}
}

func TestAsset_ValidateRejectsBlankOwnerIDs(t *testing.T) {
	blank := "   "
	a, err := NewAsset("tn-1", "server", "Host", nil)
	if err != nil {
		t.Fatal(err)
	}
	a.OwnerTeamID = &blank
	if err := a.Validate(); err == nil {
		t.Error("a blank owner_team_id was accepted")
	}
	a.OwnerTeamID = nil
	a.OwnerUserID = &blank
	if err := a.Validate(); err == nil {
		t.Error("a blank owner_user_id was accepted")
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
