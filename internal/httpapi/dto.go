package httpapi

import (
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

type createRequest struct {
	Artifact        string            `json:"artifact"`
	Version         string            `json:"version"`
	Role            string            `json:"role"`
	Lifecycle       string            `json:"lifecycle"`
	RetentionClass  string            `json:"retention_class"`
	RatifiedBy      string            `json:"ratified_by,omitempty"`
	ReviewCycle     string            `json:"review_cycle,omitempty"`
	RetentionPolicy string            `json:"retention_policy,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

func (req createRequest) toDomain() *domain.ConfigObject {
	rp := req.RetentionPolicy
	if rp == "" {
		rp = "permanent"
	}
	return &domain.ConfigObject{
		Artifact:       req.Artifact,
		Version:        req.Version,
		Role:           domain.Role(req.Role),
		Lifecycle:      domain.Lifecycle(req.Lifecycle),
		RetentionClass: domain.RetentionClass(req.RetentionClass),
		// Authority is a computed field (§6, RFC-AUTH): it is never accepted from
		// a client. At inception the artifact is in no baseline and has no
		// dependents, so §9.1 computes Non-Normative — which is also the value
		// INT-3 fixes for the inception state.
		Authority:       domain.AuthorityNonNormative,
		RatifiedBy:      req.RatifiedBy,
		ReviewCycle:     req.ReviewCycle,
		RetentionPolicy: rp,
		Metadata:        req.Metadata,
	}
}

type configObjectResponse struct {
	CfgID           string            `json:"cfg_id"`
	Artifact        string            `json:"artifact"`
	Version         string            `json:"version"`
	Role            string            `json:"role"`
	Lifecycle       string            `json:"lifecycle"`
	RetentionClass  string            `json:"retention_class"`
	Authority       string            `json:"authority"`
	RatifiedBy      string            `json:"ratified_by,omitempty"`
	ReviewCycle     string            `json:"review_cycle,omitempty"`
	RetentionPolicy string            `json:"retention_policy"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	RowVersion      int64             `json:"row_version"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

func fromDomain(o *domain.ConfigObject) configObjectResponse {
	return configObjectResponse{
		CfgID:           o.CfgID,
		Artifact:        o.Artifact,
		Version:         o.Version,
		Role:            string(o.Role),
		Lifecycle:       string(o.Lifecycle),
		RetentionClass:  string(o.RetentionClass),
		Authority:       string(o.Authority),
		RatifiedBy:      o.RatifiedBy,
		ReviewCycle:     o.ReviewCycle,
		RetentionPolicy: o.RetentionPolicy,
		Metadata:        o.Metadata,
		RowVersion:      o.RowVersion,
		CreatedAt:       o.CreatedAt,
		UpdatedAt:       o.UpdatedAt,
	}
}

// patchRequest carries only NON-CONSTITUTIONAL fields. Lifecycle, Retention and
// Authority were removed in CP-1.3: §8 states "No dimension changes except as an
// operation specifies", and Authority is additionally a computed field (§6).
// Every dimension change is reached through a §8 governance operation.
type patchRequest struct {
	RatifiedBy      *string           `json:"ratified_by,omitempty"`
	ReviewCycle     *string           `json:"review_cycle,omitempty"`
	RetentionPolicy *string           `json:"retention_policy,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type listResponse struct {
	Items      []configObjectResponse `json:"items"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

type bulkCreateRequest struct {
	Items []createRequest `json:"items"`
}

func (p patchRequest) toPatch() (*domain.Patch, error) {
	// §9.1 inputs are carried in metadata but are not descriptive data (CP-0.1).
	// Changing them would change computed Authority and the Replacement Test
	// verdict without a §8 operation and without an audit event.
	for k := range p.Metadata {
		if domain.IsConstitutionalMetadataKey(k) {
			return nil, domain.NewValidationError("metadata",
				"key "+k+" is a constitutional input (§9.1) and may not be set through patch")
		}
	}
	out := &domain.Patch{Metadata: p.Metadata}
	if p.RatifiedBy != nil {
		out.RatifiedBy = p.RatifiedBy
	}
	if p.ReviewCycle != nil {
		out.ReviewCycle = p.ReviewCycle
	}
	if p.RetentionPolicy != nil {
		if *p.RetentionPolicy == "" {
			return nil, domain.NewValidationError("retention_policy", "must not be empty")
		}
		out.RetentionPolicy = p.RetentionPolicy
	}
	return out, nil
}
