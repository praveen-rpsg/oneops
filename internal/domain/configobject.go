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
	// Cross-dimension invariant (Configuration State Model §9.3, rule 1):
	// current_baseline retention requires active authority.
	if c.RetentionClass == RetentionCurrentBaseline && c.Authority == AuthorityHistorical {
		return newValidation("retention_class", "current_baseline is incompatible with historical authority")
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
