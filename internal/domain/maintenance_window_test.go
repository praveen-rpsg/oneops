package domain

import (
	"strings"
	"testing"
	"time"
)

func TestNewMaintenanceWindow_Valid(t *testing.T) {
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	w, err := NewMaintenanceWindow("tenant-a", "asset-1", "patching", start, end)
	if err != nil {
		t.Fatalf("new maintenance window: %v", err)
	}
	if w.WindowID == "" {
		t.Error("window_id must be minted")
	}
	if w.TenantID != "tenant-a" || w.AssetID != "asset-1" || w.Reason != "patching" {
		t.Errorf("unexpected fields: %+v", w)
	}
	if !w.StartsAt.Equal(start) || !w.EndsAt.Equal(end) {
		t.Errorf("bounds = [%v, %v), want [%v, %v)", w.StartsAt, w.EndsAt, start, end)
	}
	if w.CreatedBy != "" {
		t.Error("CreatedBy must not be set by the constructor — it is the repository's job")
	}
	if w.SuppressedCount != 0 || !w.LastSuppressedAt.IsZero() {
		t.Errorf("a new window must start with no recorded suppression: %+v", w)
	}
}

func TestMaintenanceWindow_Validate(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	valid := func() *MaintenanceWindow {
		return &MaintenanceWindow{
			TenantID: "tenant-a", AssetID: "asset-1",
			StartsAt: base, EndsAt: base.Add(time.Hour),
		}
	}

	tests := []struct {
		name    string
		mutate  func(*MaintenanceWindow)
		wantErr string
	}{
		{"missing tenant", func(w *MaintenanceWindow) { w.TenantID = "" }, "tenant_id"},
		{"missing asset", func(w *MaintenanceWindow) { w.AssetID = "" }, "asset_id"},
		{"zero starts_at", func(w *MaintenanceWindow) { w.StartsAt = time.Time{} }, "starts_at"},
		{"zero ends_at", func(w *MaintenanceWindow) { w.EndsAt = time.Time{} }, "ends_at"},
		{"ends_at equal starts_at", func(w *MaintenanceWindow) { w.EndsAt = w.StartsAt }, "ends_at"},
		{"ends_at before starts_at", func(w *MaintenanceWindow) { w.EndsAt = w.StartsAt.Add(-time.Second) }, "ends_at"},
		{"reason too long", func(w *MaintenanceWindow) { w.Reason = strings.Repeat("x", MaxMaintenanceWindowReasonLength+1) }, "reason"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := valid()
			tt.mutate(w)
			err := w.Validate()
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("Validate() err = %v, want a *ValidationError", err)
			}
			if ve.Field != tt.wantErr {
				t.Errorf("Validate() field = %q, want %q", ve.Field, tt.wantErr)
			}
		})
	}

	if err := valid().Validate(); err != nil {
		t.Errorf("a well-formed window must validate: %v", err)
	}

	// The boundary itself: ends_at exactly one instant after starts_at is
	// the smallest valid, non-empty half-open window.
	w := valid()
	w.EndsAt = w.StartsAt.Add(time.Nanosecond)
	if err := w.Validate(); err != nil {
		t.Errorf("a window one instant wide must validate: %v", err)
	}
}
