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
	Authority       string            `json:"authority,omitempty"`
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
		Artifact:        req.Artifact,
		Version:         req.Version,
		Role:            domain.Role(req.Role),
		Lifecycle:       domain.Lifecycle(req.Lifecycle),
		RetentionClass:  domain.RetentionClass(req.RetentionClass),
		Authority:       domain.Authority(req.Authority),
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

type patchRequest struct {
	Lifecycle       *string           `json:"lifecycle,omitempty"`
	RetentionClass  *string           `json:"retention_class,omitempty"`
	Authority       *string           `json:"authority,omitempty"`
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
	out := &domain.Patch{Metadata: p.Metadata}
	if p.Lifecycle != nil {
		lc := domain.Lifecycle(*p.Lifecycle)
		if !lc.Valid() {
			return nil, domain.NewValidationError("lifecycle", "unknown lifecycle: "+*p.Lifecycle)
		}
		out.Lifecycle = &lc
	}
	if p.RetentionClass != nil {
		rc := domain.RetentionClass(*p.RetentionClass)
		if !rc.Valid() {
			return nil, domain.NewValidationError("retention_class", "unknown retention class: "+*p.RetentionClass)
		}
		out.RetentionClass = &rc
	}
	if p.Authority != nil {
		a := domain.Authority(*p.Authority)
		if !a.Valid() {
			return nil, domain.NewValidationError("authority", "unknown authority: "+*p.Authority)
		}
		out.Authority = &a
	}
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
