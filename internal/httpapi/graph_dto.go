package httpapi

import "github.com/rpsg/oneops/internal/domain"

// graphNode is a reachable node in a traversal response. It exposes only graph
// primitives — never authority, lifecycle, or baseline state (those are M3).
type graphNode struct {
	CfgID string `json:"cfg_id"`
	Depth int    `json:"depth"`
}

// traversalResponse is the payload for the dependencies/dependents endpoints.
type traversalResponse struct {
	Root      string      `json:"root"`
	Direction string      `json:"direction"`
	Recursive bool        `json:"recursive"`
	Count     int         `json:"count"`
	Nodes     []graphNode `json:"nodes"`
}

func newTraversalResponse(root string, dir domain.Direction, recursive bool, nodes []domain.TraversalNode) traversalResponse {
	out := make([]graphNode, len(nodes))
	for i, n := range nodes {
		out[i] = graphNode{CfgID: n.CfgID, Depth: n.Depth}
	}
	return traversalResponse{
		Root:      root,
		Direction: string(dir),
		Recursive: recursive,
		Count:     len(out),
		Nodes:     out,
	}
}

// assetGraphNode is a reachable node in a CMDB traversal response. Same shape
// as graphNode, with an asset_id key instead of cfg_id — the underlying
// domain.TraversalNode is the same generic graph primitive either way
// (ADR-ASSET-001 §4); only the JSON label differs, because "cfg_id" would be
// a confusing label on an Asset's own API.
type assetGraphNode struct {
	AssetID string `json:"asset_id"`
	Depth   int    `json:"depth"`
}

// assetTraversalResponse is the payload for the CMDB dependencies/dependents
// endpoints.
type assetTraversalResponse struct {
	Root      string           `json:"root"`
	Direction string           `json:"direction"`
	Recursive bool             `json:"recursive"`
	Count     int              `json:"count"`
	Nodes     []assetGraphNode `json:"nodes"`
}

func newAssetTraversalResponse(root string, dir domain.Direction, recursive bool, nodes []domain.TraversalNode) assetTraversalResponse {
	out := make([]assetGraphNode, len(nodes))
	for i, n := range nodes {
		out[i] = assetGraphNode{AssetID: n.CfgID, Depth: n.Depth}
	}
	return assetTraversalResponse{
		Root:      root,
		Direction: string(dir),
		Recursive: recursive,
		Count:     len(out),
		Nodes:     out,
	}
}

// serviceMapResponse is the payload for GET
// /v1/admin/assets/{id}/service-map: the supporting CIs composing a
// business_service, a projection over the CMDB graph restricted to
// depends_on/runs_on edges (E1.2).
type serviceMapResponse struct {
	ServiceID     string           `json:"service_id"`
	Count         int              `json:"count"`
	SupportingCIs []assetGraphNode `json:"supporting_cis"`
}

func newServiceMapResponse(serviceID string, nodes []domain.TraversalNode) serviceMapResponse {
	out := make([]assetGraphNode, len(nodes))
	for i, n := range nodes {
		out[i] = assetGraphNode{AssetID: n.CfgID, Depth: n.Depth}
	}
	return serviceMapResponse{ServiceID: serviceID, Count: len(out), SupportingCIs: out}
}

// cyclePath is a single detected cycle as a closed path of node ids.
type cyclePath struct {
	Path []string `json:"path"`
}

// cyclesResponse is the payload for the cycles endpoint.
type cyclesResponse struct {
	Root   string      `json:"root"`
	Count  int         `json:"count"`
	Cycles []cyclePath `json:"cycles"`
}

func newCyclesResponse(root string, cycles []domain.Cycle) cyclesResponse {
	out := make([]cyclePath, 0, len(cycles))
	for _, c := range cycles {
		out = append(out, cyclePath{Path: c.Path.Nodes})
	}
	return cyclesResponse{Root: root, Count: len(out), Cycles: out}
}
