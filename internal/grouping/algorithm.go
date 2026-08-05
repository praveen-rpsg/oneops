package grouping

import (
	"sort"

	"github.com/rpsg/oneops/internal/domain"
)

// GroupingEdgeTypes restricts the dependency walk to the two
// asset_relationship types that mean "X needs Y to function" — the identical
// filter postgres.dependencySuppressionEdgeTypes (E3.3b, ADR-ALERTING-003 §2)
// and AssetGraphRepo.RecursiveDependenciesOfTypes's own CMDB service-map
// caller already use: connected_to (a network link) and member_of (grouping)
// are excluded because a firing on X caused by a merely-connected or
// commonly-grouped peer being down is not "X's outage is collateral of Y's"
// in the sense this feature groups.
var GroupingEdgeTypes = []string{
	string(domain.RelationshipDependsOn),
	string(domain.RelationshipRunsOn),
}

// DefaultMaxDepth bounds how many hops of an asset's dependency closure
// deepestDownRoot will consider — the identical defensive perf bound
// postgres.DefaultDependencySuppressionMaxDepth applies to E3.3b's walk (the
// walk itself is already cycle-safe by construction; this caps the work done
// per candidate, not correctness).
const DefaultMaxDepth = 10

// deepestDownRoot picks E4.2's root heuristic: among nodes (assetID's own
// transitive depends_on/runs_on closure, already ordered by (depth,
// asset_id) ascending — RecursiveDependenciesOfTypes's own contract), the
// ones that are themselves "down" (present in down, keyed by asset_id,
// mapping to that asset's own open alert-incident id) are candidates; the
// DEEPEST one wins, ties broken toward the lexicographically-smaller
// asset_id.
//
// Because nodes is already sorted (depth, asset_id) ascending, "deepest,
// then lexicographically-first at that depth" falls out of a single forward
// scan that only ever replaces its best-so-far on a STRICTLY greater depth:
// the first down node encountered at a new maximum depth is, by the sort
// order, already the smallest asset_id sharing that depth — nothing later at
// the same depth can displace it.
//
// This directly produces FLAT grouping under the ultimate root with no
// further flattening step: RecursiveDependenciesOfTypes(X) already returns
// X's FULL transitive closure (not just direct dependencies), so if X depends
// on A which depends on DB, and both A and DB are down, DB (the deeper node)
// is a candidate in X's OWN closure directly — X is not first assigned A as
// an intermediate root and then re-pointed at DB in a second pass.
//
// ownAssetID is excluded defensively even though the cycle-safe traversal
// (graphtraversal.go's own doc comment) can never legitimately return the
// root of its own walk: an incident must never be handed itself as a
// candidate root.
func deepestDownRoot(nodes []domain.TraversalNode, down map[string]string, ownAssetID string) *string {
	bestDepth := -1
	var best *string
	for _, n := range nodes {
		if n.CfgID == ownAssetID {
			continue
		}
		incidentID, ok := down[n.CfgID]
		if !ok {
			continue
		}
		if n.Depth > bestDepth {
			bestDepth = n.Depth
			id := incidentID
			best = &id
		}
	}
	return best
}

// resolveFinalRoots takes each open alert-incident's independently-computed
// candidate root (candidate[incidentID], nil meaning "no down dependency
// found this tick") and returns the version actually safe to persist: never
// a self-pointer (structurally impossible from deepestDownRoot, but this is
// the last line of defense) and never a CYCLE of pointers across rows.
//
// HONEST BOUND (ADR-ALERTING-004 §5): candidate is computed independently
// per incident from that incident's OWN full transitive dependency closure,
// so it can never chain through another incident's candidate — the only way
// a cycle can appear here at all is a genuine CYCLE in the underlying CMDB
// dependency graph itself (e.g. two assets each depends_on the other, both
// currently down), which the recursive-CTE walk tolerates (it terminates,
// per graphtraversal.go) but does not resolve into one canonical root: there
// is no non-arbitrary "deepest" member of a cycle. When that happens, this
// function breaks each detected cycle deterministically by anchoring it on
// the lexicographically-smallest incidentID in the cycle (final root = nil
// for that one incident; every other member of the cycle keeps its own
// computed candidate) — self-healing (recomputed identically every tick from
// current down-state, so it converges and stays stable) and idempotent
// (re-running against an unchanged candidate map reproduces the same break
// point), never a stored decision. A genuine mutual-dependency cycle between
// two currently-firing assets is a CMDB modelling smell this feature does
// not attempt to fix — it only guarantees the RESULT stays an acyclic pointer
// structure regardless.
func resolveFinalRoots(candidate map[string]*string) map[string]*string {
	final := make(map[string]*string, len(candidate))
	// state: 0 unvisited, 1 on the current walk's path, 2 finalized.
	state := make(map[string]int, len(candidate))

	finalizePath := func(path []string, useCandidate bool, anchor string) {
		for _, id := range path {
			if !useCandidate && id == anchor {
				final[id] = nil
			} else {
				final[id] = candidate[id]
			}
			state[id] = 2
		}
	}

	visit := func(start string) {
		var path []string
		cur := start
		for {
			switch state[cur] {
			case 2:
				// cur already resolved (part of an earlier walk this pass):
				// everything queued in path merely points INTO that resolved
				// structure, not part of a cycle — each keeps its own
				// candidate.
				finalizePath(path, true, "")
				return
			case 1:
				// cur closes a cycle against an earlier entry in path.
				idx := 0
				for i, id := range path {
					if id == cur {
						idx = i
						break
					}
				}
				cycle := path[idx:]
				anchor := cycle[0]
				for _, id := range cycle[1:] {
					if id < anchor {
						anchor = id
					}
				}
				finalizePath(cycle, false, anchor)
				// Everything BEFORE the cycle in path leads into it from
				// outside and is not itself cyclic.
				finalizePath(path[:idx], true, "")
				return
			default: // unvisited
				state[cur] = 1
				path = append(path, cur)
				next := candidate[cur]
				if next == nil {
					finalizePath(path, true, "")
					return
				}
				cur = *next
			}
		}
	}

	ids := make([]string, 0, len(candidate))
	for id := range candidate {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic iteration order, reproducible across ticks/tests
	for _, id := range ids {
		if state[id] == 0 {
			visit(id)
		}
	}
	return final
}
