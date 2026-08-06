import type { IncidentDTO } from './incidents';

/** A row in the board's Table — an incident plus its resolved collateral, if any. */
export interface GroupedIncident extends IncidentDTO {
  children: GroupedIncident[];
}

/**
 * Builds root -> collateral grouping from a bounded, flat incident list,
 * client-side — see ADR-NOC-004 §2 and incidents.ts' INCIDENT_LIST_CAP doc
 * comment for the bound this relies on.
 *
 * - An incident whose `root_incident_id` names another incident IN THIS SAME
 *   LIST is rendered as that root's child, never as a top-level row.
 * - An incident with no `root_incident_id`, or whose `root_incident_id` names
 *   an incident NOT present in this list (the root fell outside the fetch's
 *   cap, or was excluded by the active status filter), is a top-level row —
 *   a true root/standalone in the first case, a degraded flat rendering of an
 *   orphaned collateral in the second. Nothing is ever dropped.
 * - Children are sorted oldest-first for a stable, deterministic order
 *   independent of the top-level sort applied afterward.
 */
export function groupIncidents(items: readonly IncidentDTO[]): GroupedIncident[] {
  const byId = new Map(items.map((inc) => [inc.incident_id, inc]));
  const childrenOf = new Map<string, IncidentDTO[]>();

  for (const inc of items) {
    if (inc.root_incident_id && byId.has(inc.root_incident_id)) {
      const siblings = childrenOf.get(inc.root_incident_id) ?? [];
      siblings.push(inc);
      childrenOf.set(inc.root_incident_id, siblings);
    }
  }

  const byCreatedAtAsc = (a: IncidentDTO, b: IncidentDTO) =>
    new Date(a.created_at).getTime() - new Date(b.created_at).getTime();

  const topLevel: GroupedIncident[] = [];
  for (const inc of items) {
    const rootIsPresent = Boolean(inc.root_incident_id && byId.has(inc.root_incident_id));
    if (rootIsPresent) continue; // rendered as a child under its root below

    const kids = (childrenOf.get(inc.incident_id) ?? []).slice().sort(byCreatedAtAsc);
    topLevel.push({ ...inc, children: kids.map((k) => ({ ...k, children: [] })) });
  }

  return topLevel;
}
