package domain

import (
	"regexp"
	"strings"
	"time"
)

// semverRe is a pragmatic semver matcher (MAJOR.MINOR.PATCH with optional
// pre-release/build), sufficient for artifact versioning.
var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// ConfigObject is the system-of-record entity for an OneOps artifact
// (Configuration State Model §6). Authority is a maintained projection.
type ConfigObject struct {
	CfgID           string
	Artifact        string
	Version         string
	Role            Role
	Lifecycle       Lifecycle
	RetentionClass  RetentionClass
	Authority       Authority
	RatifiedBy      string
	ReviewCycle     string
	RetentionPolicy string
	Metadata        map[string]string
	RowVersion      int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Validate enforces entity invariants. It returns a *ValidationError on the
// first violation, or nil when the object is well-formed.
func (c *ConfigObject) Validate() error {
	if strings.TrimSpace(c.Artifact) == "" {
		return newValidation("artifact", "must not be empty")
	}
	if len(c.Artifact) > 512 {
		return newValidation("artifact", "must be at most 512 characters")
	}
	if !semverRe.MatchString(c.Version) {
		return newValidation("version", "must be semantic version MAJOR.MINOR.PATCH")
	}
	if !c.Role.Valid() {
		return newValidation("role", "unknown role: "+string(c.Role))
	}
	if !c.Lifecycle.Valid() {
		return newValidation("lifecycle", "unknown lifecycle: "+string(c.Lifecycle))
	}
	if !c.RetentionClass.Valid() {
		return newValidation("retention_class", "unknown retention class: "+string(c.RetentionClass))
	}
	if c.Authority != "" && !c.Authority.Valid() {
		return newValidation("authority", "unknown authority: "+string(c.Authority))
	}
	if c.RetentionPolicy == "" {
		return newValidation("retention_policy", "must not be empty")
	}
	// --- §9.3 cross-dimension consistency invariants -------------------------
	//
	// §9.3 states seven. This function owns the ones decidable from a single
	// object; the status of each is recorded here so the gap is visible rather
	// than silently absent.
	//
	//  1. ENFORCED below.
	//  2. ENFORCED below, per CI-1 Issue 1.
	//  3. DELEGATED to the Authority engine (CI-1 Issue 3) — it needs
	//     SupersededBy, which is an edge in the dependency graph, not a field of
	//     this struct. A single-object validator structurally cannot decide it.
	//  4. NOT A CONSTRAINT — §9.3-4 forbids *inferring* Authority from
	//     Lifecycle == Complete. There is nothing to reject; it is satisfied by
	//     this function containing no such inference.
	//  5. ENFORCED below, per CI-1 Issue 2.
	//  6. DELEGATED to the Authority engine (CI-1 Issue 3) — its "unless an
	//     Active artifact depends on it" clause is a graph query.
	//  7. STRUCTURALLY SATISFIED — each dimension is a single-valued field, so
	//     no object can hold two values on one dimension.
	//
	// Authority is a computed field (§6), so an empty value means "not yet
	// computed" and is not a violation. The invariants below apply only when it
	// is present.

	// §9.3-1: Retention == Current Baseline ⇒ Authority == Active.
	if c.RetentionClass == RetentionCurrentBaseline && c.Authority != "" && c.Authority != AuthorityActive {
		return newValidation("retention_class",
			"current_baseline requires active authority, got "+string(c.Authority))
	}

	// §9.3-2 as interpreted by CI-1 Issue 1: Active ⇒ Lifecycle ∉ {Draft,
	// Withdrawn}. The Council ruled §9.3-2's enumeration illustrative rather
	// than exhaustive: §2 states "Authority = Active is reachable from any
	// lifecycle state except Withdrawn", and reading the enumeration strictly
	// would invalidate Suspended and In Review — and make §8 Suspension
	// ("Authority unchanged") inoperable for any Active artifact.
	if c.Authority == AuthorityActive &&
		(c.Lifecycle == LifecycleDraft || c.Lifecycle == LifecycleWithdrawn) {
		return newValidation("lifecycle",
			"active authority is incompatible with lifecycle "+string(c.Lifecycle))
	}

	// §9.3-5 as interpreted by CI-1 Issue 2: the three named archival classes
	// imply Authority ≠ Active. Audit Record is deliberately OUTSIDE the
	// invariant — that is what allows §8 Archiving ("Authority NEVER changed")
	// to archive an Active artifact without contradiction.
	if c.Authority == AuthorityActive && retentionBarredForActive(c.RetentionClass) {
		return newValidation("retention_class",
			string(c.RetentionClass)+" retention is incompatible with active authority")
	}
	return nil
}

// retentionBarredForActive reports whether §9.3-5 forbids this retention class
// for an Active artifact. Audit Record is intentionally absent: CI-1 Issue 2
// ruled it outside the invariant, and §4 defines the other three as records of
// former authority.
func retentionBarredForActive(rc RetentionClass) bool {
	switch rc {
	case RetentionHistoricalRecord, RetentionHistoricalEvidence, RetentionSupersededPlan:
		return true
	default:
		return false
	}
}

// ValidateInception reports whether this object is in the state INT-3 fixes for
// a newly created Configuration Object:
//
//	"A Configuration Object exists for every artifact from inception, in an
//	 unratified state (Lifecycle = Draft/In-Progress · Authority = Non-Normative
//	 · Retention = Working Material). Existence ≠ Ratification; implementation
//	 creates an artifact, never authority."
//
// It is deliberately SEPARATE from Validate(). Validate() runs on the result of
// every §8 transition, where ratified / current_baseline / active is the correct
// outcome; folding inception into it would make Ratification impossible. This
// constrains creation only.
//
// It lives here rather than in a transport layer so that every caller observes
// it — HTTP today, and any in-process caller that constructs a ConfigObject
// directly (ADR-CP5).
func (c *ConfigObject) ValidateInception() error {
	switch c.Lifecycle {
	case LifecycleDraft, LifecycleInProgress:
	default:
		return newValidation("lifecycle",
			"at inception must be draft or in_progress (INT-3), got "+string(c.Lifecycle))
	}
	if c.RetentionClass != RetentionWorkingMaterial {
		return newValidation("retention_class",
			"at inception must be working_material (INT-3), got "+string(c.RetentionClass))
	}
	// Authority is computed (§6); an unset value is the normal case at creation.
	// A caller that sets it to anything other than the inception value is
	// asserting authority, which INT-3 forbids.
	if c.Authority != "" && c.Authority != AuthorityNonNormative {
		return newValidation("authority",
			"at inception must be non_normative (INT-3), got "+string(c.Authority))
	}
	// §9.1 inputs are carried in the metadata map but are not descriptive data.
	// They decide computed Authority and the four-part Replacement Test, so a
	// caller that supplies them at creation is deciding a constitutional verdict
	// with an unaudited client field. Patch already refuses them; creation did
	// not, and a successor seeded with crafted `responsibilities` turned a
	// Replacement that the engine refused (409) into one it granted (200) against
	// a ratified current_baseline artifact (ADR-GOV-003).
	for k := range c.Metadata {
		if IsConstitutionalMetadataKey(k) {
			return newValidation("metadata",
				"key "+k+" is a constitutional input (§9.1) and may not be set at creation")
		}
	}
	return nil
}

// ArtifactVersion records an immutable rendered body for a ConfigObject version.
type ArtifactVersion struct {
	CfgID       string
	Version     string
	BodyURI     string
	ContentHash string
	Format      Format
	CreatedAt   time.Time
}

// Validate enforces ArtifactVersion invariants.
func (a *ArtifactVersion) Validate() error {
	if strings.TrimSpace(a.CfgID) == "" {
		return newValidation("cfg_id", "must not be empty")
	}
	if strings.TrimSpace(a.BodyURI) == "" {
		return newValidation("body_uri", "must not be empty")
	}
	if len(a.ContentHash) != 64 {
		return newValidation("content_hash", "must be a hex sha256 (64 chars)")
	}
	if !a.Format.Valid() {
		return newValidation("format", "unknown format: "+string(a.Format))
	}
	return nil
}
