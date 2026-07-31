package domain

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SettingType is the primitive a setting's stored value is validated against.
type SettingType string

const (
	SettingTypeBool   SettingType = "bool"
	SettingTypeInt    SettingType = "int"
	SettingTypeString SettingType = "string"
	SettingTypeEnum   SettingType = "enum"
)

// SettingDefinition describes one allowed setting: its type, default and the
// validation a stored value must satisfy.
//
// This is an in-code allowlist, not a schema-less column: a tenant may only
// override a key defined here, and only with a value that validates against
// it. Without this a settings table becomes a junk drawer — any key, any
// value, and nothing in the platform that can say what a stored row means or
// whether the process that reads it would even recognise it.
type SettingDefinition struct {
	Key         string
	Type        SettingType
	Default     string
	Description string
	// Enum lists the allowed values when Type is SettingTypeEnum. Unused
	// otherwise.
	Enum []string
	// Min and Max bound an int value inclusively. Meaningful only when Type is
	// SettingTypeInt; every int definition below sets both, so "unbounded" is
	// never a silent default.
	Min, Max int64
	// MaxLen bounds a string value's length. Meaningful only when Type is
	// SettingTypeString; zero means unbounded.
	MaxLen int
}

// Validate reports whether raw is a legal stored value for this definition.
// It operates on the canonical string form ("true"/"false", a base-10
// integer, or the raw string/enum value) — the same form Setting.Value and
// SettingDefinition.Default carry.
func (d SettingDefinition) Validate(raw string) error {
	switch d.Type {
	case SettingTypeBool:
		if raw != "true" && raw != "false" {
			return newValidation(d.Key, `must be "true" or "false"`)
		}
		return nil
	case SettingTypeInt:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return newValidation(d.Key, "must be an integer")
		}
		if n < d.Min || n > d.Max {
			return newValidation(d.Key, fmt.Sprintf("must be between %d and %d", d.Min, d.Max))
		}
		return nil
	case SettingTypeEnum:
		for _, v := range d.Enum {
			if raw == v {
				return nil
			}
		}
		return newValidation(d.Key, "must be one of: "+strings.Join(d.Enum, ", "))
	case SettingTypeString:
		if strings.TrimSpace(raw) == "" {
			return newValidation(d.Key, "must not be empty")
		}
		if d.MaxLen > 0 && len(raw) > d.MaxLen {
			return newValidation(d.Key, fmt.Sprintf("must be at most %d characters", d.MaxLen))
		}
		return nil
	default:
		// Reachable only if a definition below is malformed; a defect in the
		// registry, not in a caller's request.
		return newValidation(d.Key, "has no recognised type; the definition itself is invalid")
	}
}

// EncodeValue converts a JSON-decoded request value (bool, float64 or string,
// as produced by decoding into interface{}) into this definition's canonical
// string form. It checks shape only — whether the JSON value is even the
// right kind of thing — and leaves range/enum/length checking to Validate,
// which every caller must still run on the result.
func (d SettingDefinition) EncodeValue(v any) (string, error) {
	switch d.Type {
	case SettingTypeBool:
		b, ok := v.(bool)
		if !ok {
			return "", newValidation(d.Key, "must be a boolean")
		}
		return strconv.FormatBool(b), nil
	case SettingTypeInt:
		f, ok := v.(float64)
		if !ok {
			return "", newValidation(d.Key, "must be an integer")
		}
		if f != float64(int64(f)) {
			return "", newValidation(d.Key, "must be an integer, not a fraction")
		}
		return strconv.FormatInt(int64(f), 10), nil
	case SettingTypeString, SettingTypeEnum:
		s, ok := v.(string)
		if !ok {
			return "", newValidation(d.Key, "must be a string")
		}
		return s, nil
	default:
		return "", newValidation(d.Key, "has no recognised type; the definition itself is invalid")
	}
}

// DecodeValue converts a canonical stored string back into a typed value for
// a JSON response — the inverse of EncodeValue's bool/int handling. raw is
// assumed already valid (it either came from Default or passed Validate on
// the way in), so this never fails.
func (d SettingDefinition) DecodeValue(raw string) any {
	switch d.Type {
	case SettingTypeBool:
		return raw == "true"
	case SettingTypeInt:
		n, _ := strconv.ParseInt(raw, 10, 64)
		return n
	default:
		return raw
	}
}

// SettingDefinitions is the platform's allowlist of overridable settings.
// A key absent here can never be stored — GetDefinition and every Setting
// constructor refuse it — which is what keeps this a bounded set of things the
// platform actually reads rather than an open key-value store.
//
// Choosing what belongs here is a product decision as much as a technical one:
// each entry mirrors a real, already-typed knob elsewhere in the platform
// (pagination defaults, notification retry/channel behaviour, webhook
// delivery retention), rather than inventing new ones settings alone would
// need to justify.
var SettingDefinitions = map[string]SettingDefinition{
	"default_page_size": {
		Key:         "default_page_size",
		Type:        SettingTypeInt,
		Default:     "50",
		Description: "Default page size for keyset-paginated list endpoints, absent an explicit limit.",
		Min:         1, Max: 500,
	},
	"notification_default_channel": {
		Key:         "notification_default_channel",
		Type:        SettingTypeEnum,
		Default:     string(NotificationEmail),
		Description: "Channel used for a notification whose producer does not specify one.",
		Enum:        []string{string(NotificationEmail), string(NotificationWebhook), string(NotificationInApp)},
	},
	"notification_max_attempts": {
		Key:         "notification_max_attempts",
		Type:        SettingTypeInt,
		Default:     "5",
		Description: "How many times the notification worker retries a failed delivery before the retry budget is exhausted.",
		Min:         1, Max: 20,
	},
	"webhook_delivery_retention_days": {
		Key:         "webhook_delivery_retention_days",
		Type:        SettingTypeInt,
		Default:     "30",
		Description: "How long a terminal webhook delivery (delivered or dead_letter) is retained before the retention worker deletes it.",
		Min:         1, Max: 365,
	},
	"webhooks_enabled_by_default": {
		Key:         "webhooks_enabled_by_default",
		Type:        SettingTypeBool,
		Default:     "true",
		Description: "Whether a newly registered webhook starts enabled.",
	},
	"notification_from_name": {
		Key:         "notification_from_name",
		Type:        SettingTypeString,
		Default:     "OneOps",
		Description: "Display name attached to an outbound notification.",
		MaxLen:      MaxNotificationSubjectLength,
	},
}

// GetDefinition returns the definition for key, and whether it exists.
func GetDefinition(key string) (SettingDefinition, bool) {
	d, ok := SettingDefinitions[key]
	return d, ok
}

// Setting is one tenant's stored override of a defined setting. Its absence
// for a given key is not an error state — it means the tenant has not
// overridden that key, and the effective value is the definition's default.
//
// TenantID is a field on the struct rather than resolved internally, exactly
// as Team.TenantID is: the only correct source is the caller's bound
// connection, and a repository over that connection still needs an actual
// value to put in the column (ADR-TENANCY-002).
type Setting struct {
	TenantID string
	Key      string
	// Value is the canonical stored form: "true"/"false" for bool, a base-10
	// integer for int, the raw value for string and enum.
	Value      string
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Validate enforces the invariants the database also enforces: the key must
// be one this platform defines, and the value must be legal for it. This is
// what turns an unknown key or an invalid value into a field-level 422 rather
// than a constraint violation surfacing as a 500.
func (s *Setting) Validate() error {
	if strings.TrimSpace(s.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	def, ok := GetDefinition(s.Key)
	if !ok {
		return newValidation("key", fmt.Sprintf("%q is not a defined setting", s.Key))
	}
	return def.Validate(s.Value)
}

// NewSetting builds a setting override for the given tenant. rowVersion is the
// version the caller last read; 0 means "no override exists yet — create one".
func NewSetting(tenantID, key, value string, rowVersion int64) (*Setting, error) {
	s := &Setting{
		TenantID:   strings.TrimSpace(tenantID),
		Key:        strings.TrimSpace(key),
		Value:      value,
		RowVersion: rowVersion,
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// EffectiveSetting is one definition's resolved value for a tenant: the
// definition's default, unless a stored override exists.
type EffectiveSetting struct {
	Key         string
	Type        SettingType
	Description string
	Value       string
	Default     string
	Overridden  bool
	// RowVersion is the override's current version, 0 when not Overridden — a
	// caller must read this before calling Upsert to change it, exactly as
	// Team.RowVersion works for PatchTeamRequest.
	RowVersion int64
	UpdatedAt  time.Time
}

// ResolveSettings overlays a tenant's stored overrides onto SettingDefinitions
// to produce one EffectiveSetting per definition.
//
// Output is sorted by key. Nothing here is derived from the network, the
// clock's current instant beyond an override's own UpdatedAt, or the process
// environment — the same determinism discipline PKG extractors are held to —
// so two calls with the same overrides always render in the same order.
func ResolveSettings(overrides []*Setting) []EffectiveSetting {
	byKey := make(map[string]*Setting, len(overrides))
	for _, o := range overrides {
		byKey[o.Key] = o
	}

	keys := make([]string, 0, len(SettingDefinitions))
	for k := range SettingDefinitions {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]EffectiveSetting, 0, len(keys))
	for _, k := range keys {
		def := SettingDefinitions[k]
		es := EffectiveSetting{
			Key: k, Type: def.Type, Description: def.Description,
			Value: def.Default, Default: def.Default,
		}
		if o, ok := byKey[k]; ok {
			es.Value = o.Value
			es.Overridden = true
			es.RowVersion = o.RowVersion
			es.UpdatedAt = o.UpdatedAt
		}
		out = append(out, es)
	}
	return out
}

// SettingRepository administers a tenant's setting overrides.
//
// setting is TENANT-OWNED: it is in TenantOwnedTables and carries row-level
// security, so GetAll takes no tenant argument — the bound connection already
// is the boundary (ADR-TENANCY-002), the same shape TeamRepository uses.
// Upsert still carries TenantID on the record because the column needs an
// actual value to write, exactly as Team and TeamMembership do.
//
// Mutations here do NOT pass through withAdminAudit. ADR-AUDIT-007 §6.2 scopes
// that chokepoint to five named identity-governance tables (app_user,
// organization, membership, invitation, tenant); setting is application
// configuration, not one of them, so it follows the same pattern as team,
// webhook and policy: tenant-owned, RLS-isolated, and optimistically locked,
// without the administrative chokepoint.
type SettingRepository interface {
	// GetAll returns every stored override for the bound tenant. Zero rows is
	// not an error — it means the tenant has overridden nothing, and every
	// setting resolves to its definition's default.
	GetAll(ctx context.Context) ([]*Setting, error)
	// Upsert creates or updates one setting override under optimistic locking.
	// s.RowVersion is the version the caller last read; 0 means "create — no
	// override exists yet". Passing 0 against a key that already has an
	// override, or a nonzero version that no longer matches the stored row,
	// fails with ErrVersionMismatch.
	Upsert(ctx context.Context, s *Setting) (*Setting, error)
}
