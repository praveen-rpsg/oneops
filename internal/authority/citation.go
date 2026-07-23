package authority

import (
	"context"
	"sort"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/rpsg/oneops/internal/domain"
)

// ArtifactCitationEvaluator answers the graph-independent Replacement-Test clause
// noActiveArtifactCites(): is a superseded configuration still cited by an ACTIVE
// artifact? Citations are opaque logical identifiers read only from Configuration
// Object metadata (never inferred from names, scanned from source, or read from
// documents); comparison is deterministic. "Active" reuses the M3.1 resolver's
// active set exactly as the M3.2 active-dependency clause does — a configuration
// reachable from the in-force baseline. It implements no operational gap
// analysis, replacement workflow, governance, persistence, or cache; those
// remain deferred.
type ArtifactCitationEvaluator struct {
	resolver *Resolver
	data     domain.CitationGraph
}

// NewArtifactCitationEvaluator builds an evaluator over an existing resolver (for
// the active set and object existence) and a metadata-backed citation source.
func NewArtifactCitationEvaluator(resolver *Resolver, data domain.CitationGraph) *ArtifactCitationEvaluator {
	return &ArtifactCitationEvaluator{resolver: resolver, data: data}
}

// citationsOf returns the validated, sorted citation ids declared by cfgID.
// Whitespace-only or empty segments are malformed metadata; duplicate ids are
// rejected; every cited id must reference an existing object. Each is a
// structured *ValidationError — never a silent failure.
func (e *ArtifactCitationEvaluator) citationsOf(ctx context.Context, cfgID string) ([]string, error) {
	raw, err := e.data.Citations(ctx, cfgID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		id := strings.TrimSpace(c)
		if id == "" {
			return nil, &ValidationError{Kind: "malformed_citation_metadata", CfgID: cfgID, Detail: "citation metadata contains an empty id"}
		}
		if seen[id] {
			return nil, &ValidationError{Kind: "duplicate_citation", CfgID: cfgID, Detail: "citation " + id + " declared more than once"}
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	for _, id := range out {
		exists, err := e.data.ObjectExists(ctx, id)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, &ValidationError{Kind: "unknown_artifact_reference", CfgID: cfgID, Detail: "cited object " + id + " does not exist"}
		}
	}
	return out, nil
}

// activeArtifactCitersOf returns the ACTIVE artifacts (members of the precomputed
// active set) whose metadata cites target, sorted and deduplicated. A config does
// not keep itself Active by citing itself. It is a pure function over the citation
// source and a precomputed active set — no traversal modification, no resolver
// state. Every scanned active artifact's citations are validated, so malformed or
// dangling citation metadata in an active artifact surfaces as a *ValidationError
// rather than a silent wrong answer.
func (e *ArtifactCitationEvaluator) activeArtifactCitersOf(ctx context.Context, target string, active map[string]reachInfo) ([]string, error) {
	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic scan order → deterministic, sorted result
	var citers []string
	for _, a := range ids {
		if a == target {
			continue
		}
		cites, err := e.citationsOf(ctx, a)
		if err != nil {
			return nil, err
		}
		for _, c := range cites {
			if c == target {
				citers = append(citers, a)
				break
			}
		}
	}
	return citers, nil
}

// EvaluateCitations reports whether cfgID is still cited by an ACTIVE artifact,
// with the citing artifacts as evidence. A missing object yields a
// *ValidationError.
func (e *ArtifactCitationEvaluator) EvaluateCitations(ctx context.Context, cfgID string) (domain.ActiveCitationResult, error) {
	ctx, span := tracer.Start(ctx, "ArtifactCitationEvaluator.EvaluateCitations",
		trace.WithAttributes(attribute.String("authority.cfg_id", cfgID)))
	defer span.End()

	exists, err := e.resolver.data.ObjectExists(ctx, cfgID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "exists")
		return domain.ActiveCitationResult{}, err
	}
	if !exists {
		return domain.ActiveCitationResult{}, &ValidationError{Kind: "missing_object", CfgID: cfgID, Detail: "object does not exist"}
	}
	active, err := e.resolver.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return domain.ActiveCitationResult{}, err
	}
	span.SetAttributes(attribute.Int("authority.active_set_size", len(active)))
	citers, err := e.activeArtifactCitersOf(ctx, cfgID, active)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "evaluate")
		return domain.ActiveCitationResult{}, err
	}
	span.SetAttributes(
		attribute.Bool("authority.has_active_citation", len(citers) > 0),
		attribute.Int("authority.citing_count", len(citers)),
	)
	return domain.ActiveCitationResult{
		CfgID:             cfgID,
		HasActiveCitation: len(citers) > 0,
		CitingArtifacts:   citers,
	}, nil
}

// EvaluateReplacement reports whether replacing oldCfgID with newCfgID clears the
// active citations to oldCfgID: any ACTIVE artifact still citing oldCfgID blocks
// the replacement. An empty target or a non-existent old/new object yields a
// *ValidationError (broken replacement mapping). It performs no approval or gap
// analysis.
func (e *ArtifactCitationEvaluator) EvaluateReplacement(ctx context.Context, oldCfgID, newCfgID string) (domain.CitationReplacementResult, error) {
	ctx, span := tracer.Start(ctx, "ArtifactCitationEvaluator.EvaluateReplacement",
		trace.WithAttributes(attribute.String("authority.old", oldCfgID), attribute.String("authority.new", newCfgID)))
	defer span.End()

	if strings.TrimSpace(newCfgID) == "" {
		return domain.CitationReplacementResult{}, &ValidationError{Kind: "empty_replacement_target", CfgID: oldCfgID, Detail: "replacement target is empty"}
	}
	for _, id := range []string{oldCfgID, newCfgID} {
		exists, err := e.resolver.data.ObjectExists(ctx, id)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "exists")
			return domain.CitationReplacementResult{}, err
		}
		if !exists {
			return domain.CitationReplacementResult{}, &ValidationError{Kind: "broken_replacement", CfgID: id, Detail: "object does not exist"}
		}
	}
	active, err := e.resolver.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return domain.CitationReplacementResult{}, err
	}
	remaining, err := e.activeArtifactCitersOf(ctx, oldCfgID, active)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "evaluate")
		return domain.CitationReplacementResult{}, err
	}
	span.SetAttributes(attribute.Bool("authority.citation_cleared", len(remaining) == 0))
	return domain.CitationReplacementResult{
		OldCfgID:  oldCfgID,
		NewCfgID:  newCfgID,
		Cleared:   len(remaining) == 0,
		Remaining: remaining,
	}, nil
}

// EvaluateBatch evaluates many objects, sharing one active-set computation.
// Results are returned in input order (deterministic).
func (e *ArtifactCitationEvaluator) EvaluateBatch(ctx context.Context, cfgIDs []string) ([]domain.ActiveCitationResult, error) {
	ctx, span := tracer.Start(ctx, "ArtifactCitationEvaluator.EvaluateBatch",
		trace.WithAttributes(attribute.Int("authority.batch_size", len(cfgIDs))))
	defer span.End()

	active, err := e.resolver.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return nil, err
	}
	out := make([]domain.ActiveCitationResult, 0, len(cfgIDs))
	for _, id := range cfgIDs {
		exists, err := e.resolver.data.ObjectExists(ctx, id)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "exists")
			return nil, err
		}
		if !exists {
			return nil, &ValidationError{Kind: "missing_object", CfgID: id, Detail: "object does not exist"}
		}
		citers, err := e.activeArtifactCitersOf(ctx, id, active)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "evaluate")
			return nil, err
		}
		out = append(out, domain.ActiveCitationResult{
			CfgID:             id,
			HasActiveCitation: len(citers) > 0,
			CitingArtifacts:   citers,
		})
	}
	span.SetAttributes(attribute.Int("authority.resolved_count", len(out)))
	return out, nil
}
