package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNewIncident_BuildsAnOpenIncident(t *testing.T) {
	inc, err := NewIncident(" tn-1 ", " db down ", "primary is unreachable", IncidentSeverityCritical, nil, nil)
	if err != nil {
		t.Fatalf("NewIncident: %v", err)
	}
	if inc.TenantID != "tn-1" || inc.Title != "db down" {
		t.Errorf("identifiers/title not trimmed: %+v", inc)
	}
	if inc.Status != IncidentOpen {
		t.Errorf("status = %q, want open", inc.Status)
	}
	if !inc.Open() {
		t.Error("a new incident must be open")
	}
	if inc.IncidentID == "" {
		t.Error("incident_id must be minted")
	}
}

func TestNewIncident_RequiresTenantTitleAndSeverity(t *testing.T) {
	for _, c := range []struct {
		name          string
		tenant, title string
		severity      IncidentSeverity
	}{
		{"no tenant", "", "title", IncidentSeverityHigh},
		{"blank tenant", "   ", "title", IncidentSeverityHigh},
		{"no title", "tn-1", "", IncidentSeverityHigh},
		{"blank title", "tn-1", "   ", IncidentSeverityHigh},
		{"invalid severity", "tn-1", "title", IncidentSeverity("catastrophic")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewIncident(c.tenant, c.title, "", c.severity, nil, nil); err == nil {
				t.Error("an incomplete incident was constructed")
			}
		})
	}
}

func TestNewIncident_RejectsAnOverlongTitle(t *testing.T) {
	if _, err := NewIncident("tn-1", strings.Repeat("x", MaxIncidentTitleLength+1), "", IncidentSeverityLow, nil, nil); err == nil {
		t.Error("a title over the length bound was accepted")
	}
}

func TestNewIncident_RejectsAnOverlongDescription(t *testing.T) {
	if _, err := NewIncident("tn-1", "title", strings.Repeat("x", MaxIncidentDescriptionLength+1), IncidentSeverityLow, nil, nil); err == nil {
		t.Error("a description over the length bound was accepted")
	}
}

func TestIncidentSeverity_Valid(t *testing.T) {
	for _, s := range []IncidentSeverity{IncidentSeverityCritical, IncidentSeverityHigh, IncidentSeverityMedium, IncidentSeverityLow} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if IncidentSeverity("unknown").Valid() {
		t.Error(`"unknown" must not be a valid severity — an incident always states impact`)
	}
}

func TestIncidentStatus_Valid(t *testing.T) {
	for _, s := range []IncidentStatus{
		IncidentOpen, IncidentAcknowledged, IncidentInvestigating,
		IncidentResolved, IncidentClosed, IncidentReopened,
	} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if IncidentStatus("bogus").Valid() {
		t.Error(`"bogus" should not be valid`)
	}
}

// The whole legal lifecycle: every documented forward edge, plus the
// resolved -> reopened -> investigating loop, succeeds.
func TestIncidentStatus_CanTransitionTo_LegalEdges(t *testing.T) {
	cases := []struct{ from, to IncidentStatus }{
		{IncidentOpen, IncidentAcknowledged},
		{IncidentAcknowledged, IncidentInvestigating},
		{IncidentInvestigating, IncidentResolved},
		{IncidentResolved, IncidentClosed},
		{IncidentResolved, IncidentReopened},
		{IncidentReopened, IncidentInvestigating},
	}
	for _, c := range cases {
		if !c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be legal", c.from, c.to)
		}
	}
}

// THIS MUST BITE: every move not explicitly listed above is refused,
// including skipping states, moving backward, and self-transitions.
func TestIncidentStatus_CanTransitionTo_RejectsIllegalEdges(t *testing.T) {
	cases := []struct{ from, to IncidentStatus }{
		{IncidentOpen, IncidentInvestigating},         // skip acknowledged
		{IncidentOpen, IncidentResolved},              // skip two states
		{IncidentOpen, IncidentClosed},                // skip the whole pipeline
		{IncidentOpen, IncidentOpen},                  // self-transition (no-op)
		{IncidentAcknowledged, IncidentOpen},          // backward
		{IncidentAcknowledged, IncidentResolved},      // skip investigating
		{IncidentInvestigating, IncidentAcknowledged}, // backward
		{IncidentInvestigating, IncidentClosed},       // skip resolved
		{IncidentResolved, IncidentInvestigating},     // must go through reopened
		{IncidentResolved, IncidentOpen},
		{IncidentClosed, IncidentOpen},     // terminal
		{IncidentClosed, IncidentReopened}, // terminal
		{IncidentClosed, IncidentClosed},
		{IncidentReopened, IncidentResolved}, // must re-earn it via investigating
		{IncidentReopened, IncidentClosed},
		{IncidentReopened, IncidentOpen},
	}
	for _, c := range cases {
		if c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be illegal", c.from, c.to)
		}
	}
}

func TestNewIncidentTransitionError_UnwrapsToErrInvalidTransition(t *testing.T) {
	err := NewIncidentTransitionError(IncidentOpen, IncidentClosed)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Error("must unwrap to ErrInvalidTransition")
	}
	var target *IncidentTransitionError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed")
	}
	if target.From != IncidentOpen || target.To != IncidentClosed {
		t.Errorf("From/To = %s/%s, want open/closed", target.From, target.To)
	}
}

func TestIncidentEventKind_Valid(t *testing.T) {
	for _, k := range []IncidentEventKind{IncidentEventCreated, IncidentEventStatusTransitioned, IncidentEventAssigned} {
		if !k.Valid() {
			t.Errorf("%q should be valid", k)
		}
	}
	if IncidentEventKind("commented").Valid() {
		t.Error(`"commented" has no write path in E5.1 and must not validate as a storable kind yet`)
	}
}

// AssetID/AssigneeUserID follow the same tri-state-friendly "not blank when
// supplied" rule Asset.OwnerTeamID/OwnerUserID carry.
func TestIncident_Validate_RejectsBlankOptionalReferences(t *testing.T) {
	blank := "   "
	inc, err := NewIncident("tn-1", "title", "", IncidentSeverityLow, nil, nil)
	if err != nil {
		t.Fatalf("NewIncident: %v", err)
	}
	inc.AssetID = &blank
	if err := inc.Validate(); err == nil {
		t.Error("a blank asset_id should be rejected")
	}
	inc.AssetID = nil
	inc.AssigneeUserID = &blank
	if err := inc.Validate(); err == nil {
		t.Error("a blank assignee_user_id should be rejected")
	}
}
