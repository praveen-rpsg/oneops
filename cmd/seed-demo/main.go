// Command seed-demo populates a running control plane with a coherent,
// realistic demo dataset — a small SaaS company's estate on an operational +
// security "day" — purely through the EXISTING /v1/admin/* HTTP API, exactly
// as any other API client would. It is a demo/dev tool, not product code: it
// owns no database connection, mints no credentials, and makes no security
// decision of its own — every write goes through the same authorization,
// validation and tenant-isolation path a real console session would.
//
// Run against a fresh control plane (system tenant, auth disabled):
//
//	ONEOPS_AUTH_ENABLED=false go run ./cmd/controlplane   # terminal 1
//	make seed-demo                                        # terminal 2
//	open http://localhost:8080                            # the console
//
// or directly:
//
//	go run ./cmd/seed-demo -base http://localhost:8080
//
// Set ONEOPS_TOKEN to a bearer token for a real IdP-authed run (omit it when
// auth is disabled). ONEOPS_BASE_URL / -base override the target.
//
// Idempotency: this assumes a FRESH tenant, e.g. right after `make db-reset`.
// A 409 from a create that has a natural uniqueness constraint (user email,
// ioc indicator, compliance control ref, ...) is logged and skipped rather
// than treated as fatal, so a re-run does not hard-fail — but this is not a
// general-purpose idempotent seeder (a re-run duplicates assets, incidents,
// findings, etc., which have no such constraint).
//
// Its own outbound client is built with safehttp.Client — the one
// constructor internal/arch's SSRF guard (ADR-SECURITY-001/003) allows
// anywhere in this tree — with the private-address guard explicitly opted
// out: this tool's own target is an operator-supplied local/demo endpoint
// (default localhost), never a tenant-supplied one, which is the exact case
// that constructor's own doc comment carves out for the opt-in.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/rpsg/oneops/internal/safehttp"
)

func main() {
	base := flag.String("base", envOr("ONEOPS_BASE_URL", "http://localhost:8080"), "control-plane base URL")
	flag.Parse()

	c := &apiClient{
		http:  safehttp.Client(30*time.Second, true),
		base:  *base,
		token: os.Getenv("ONEOPS_TOKEN"),
	}
	ctx := context.Background()
	sum := &summary{}

	log.Printf("seeding demo data against %s ...", c.base)

	assetID := seedAssets(ctx, c, sum)
	seedOrgAndUsers(ctx, c, sum) // populates sum.userIDs
	userID := sum.userIDs
	seedAlertRules(ctx, c, sum, assetID)
	seedIncidents(ctx, c, sum, assetID, userID)
	scheduleID := seedOnCall(ctx, c, sum, userID)
	seedEscalation(ctx, c, sum, scheduleID)
	seedMaintenanceWindow(ctx, c, sum, assetID)
	seedVulnFindings(ctx, c, sum, assetID)
	seedIOCs(ctx, c, sum)
	seedSecurityRuleAndObservations(ctx, c, sum, assetID)
	seedRisks(ctx, c, sum, assetID)
	seedCompliance(ctx, c, sum)
	seedSecurityResponseRule(ctx, c, sum)

	sum.print(c.base)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

type apiClient struct {
	http  *http.Client
	base  string
	token string
}

// maxRateLimitRetries/rateLimitBackoff back this tool off ADR-SECURITY-004's
// per-tenant token bucket (the system tenant's default is a modest 20 rps /
// 40 burst) instead of either outrunning it or forcing an operator to raise
// it just to run a demo seeder. A 429 here is the platform behaving exactly
// as designed, not a fatal surprise, so it is retried a bounded number of
// times before this tool gives up and treats it like any other bad status.
const (
	maxRateLimitRetries = 10
	rateLimitBackoff    = 300 * time.Millisecond
)

// do issues one request, transparently retrying a 429, and returns its final
// status code and raw response body.
func (c *apiClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal %s %s: %w", method, path, err)
		}
		raw = b
	}

	var (
		status int
		data   []byte
	)
	for attempt := 0; ; attempt++ {
		var rdr io.Reader
		if raw != nil {
			rdr = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
		if err != nil {
			return 0, nil, fmt.Errorf("build %s %s: %w", method, path, err)
		}
		if raw != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
		}
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return resp.StatusCode, nil, fmt.Errorf("%s %s: read body: %w", method, path, err)
		}
		status = resp.StatusCode
		if status != http.StatusTooManyRequests || attempt >= maxRateLimitRetries {
			break
		}
		log.Printf("RATE  %-6s %-55s -> 429, backing off %s (attempt %d/%d)",
			method, path, rateLimitBackoff, attempt+1, maxRateLimitRetries)
		time.Sleep(rateLimitBackoff)
	}
	return status, data, nil
}

// call is the one place that decides whether a response is the happy path,
// a tolerated 409 (fresh-tenant re-run), or a fatal surprise. A 422/400 on
// the happy path is exactly the class of bug this tool must never paper
// over, so anything outside {200, 201, and — when tolerate409 — 409} exits
// the process loudly with the request and response bodies attached.
func (c *apiClient) call(ctx context.Context, method, path string, body any, tolerate409 bool) (map[string]any, bool) {
	status, data, err := c.do(ctx, method, path, body)
	if err != nil {
		log.Fatalf("%s %s: %v", method, path, err)
	}
	if tolerate409 && status == http.StatusConflict {
		log.Printf("SKIP  %-6s %-55s -> 409 conflict (%s)", method, path, data)
		return nil, false
	}
	if status != http.StatusOK && status != http.StatusCreated {
		log.Fatalf("%s %s: unexpected status %d\n  request:  %s\n  response: %s",
			method, path, status, mustJSON(body), data)
	}
	var out map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			log.Fatalf("%s %s: decode response: %v (body: %s)", method, path, err, data)
		}
	}
	return out, true
}

// post creates a resource, tolerating a 409 from a natural uniqueness
// constraint (fresh-tenant re-run). ok is false when the create was skipped.
func (c *apiClient) post(ctx context.Context, path string, body any) (out map[string]any, ok bool) {
	return c.call(ctx, http.MethodPost, path, body, true)
}

// mustPost is post without 409 tolerance — for calls this script's own
// dependency order guarantees are novel (a transition, an assignment, an
// evidence append), where a conflict would mean the script itself is wrong.
func (c *apiClient) mustPost(ctx context.Context, path string, body any) map[string]any {
	out, _ := c.call(ctx, http.MethodPost, path, body, false)
	return out
}

func (c *apiClient) mustPatch(ctx context.Context, path string, body any) map[string]any {
	out, _ := c.call(ctx, http.MethodPatch, path, body, false)
	return out
}

func (c *apiClient) mustGet(ctx context.Context, path string) map[string]any {
	out, _ := c.call(ctx, http.MethodGet, path, nil, false)
	return out
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<unmarshalable: %v>", err)
	}
	return string(b)
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ---------------------------------------------------------------------------
// Summary
// ---------------------------------------------------------------------------

type summary struct {
	assets, relationships                          int
	alertRules                                     int
	incidents, incidentTransitions, incidentAssign int
	users, memberships                             int
	onCallSchedules, onCallParticipants            int
	escalationPolicies, escalationTiers            int
	maintenanceWindows                             int
	vulnFindings                                   int
	iocs                                           int
	securityRules, securityObservations            int
	risks                                          int
	complianceControls, controlEvidence            int
	securityResponseRules                          int

	userIDs map[string]string
}

func (s *summary) print(base string) {
	fmt.Println()
	fmt.Println("demo dataset seeded:")
	fmt.Printf("  assets:                   %d (+%d relationships)\n", s.assets, s.relationships)
	fmt.Printf("  alert rules:              %d\n", s.alertRules)
	fmt.Printf("  incidents:                %d (%d transitions, %d assignments)\n",
		s.incidents, s.incidentTransitions, s.incidentAssign)
	fmt.Printf("  users:                    %d (%d memberships)\n", s.users, s.memberships)
	fmt.Printf("  on-call schedules:        %d (%d participants)\n", s.onCallSchedules, s.onCallParticipants)
	fmt.Printf("  escalation policies:      %d (%d tiers)\n", s.escalationPolicies, s.escalationTiers)
	fmt.Printf("  maintenance windows:      %d\n", s.maintenanceWindows)
	fmt.Printf("  vuln findings:            %d\n", s.vulnFindings)
	fmt.Printf("  iocs:                     %d\n", s.iocs)
	fmt.Printf("  security rules:           %d\n", s.securityRules)
	fmt.Printf("  security observations:    %d\n", s.securityObservations)
	fmt.Printf("  risks:                    %d\n", s.risks)
	fmt.Printf("  compliance controls:      %d (%d evidence records)\n", s.complianceControls, s.controlEvidence)
	fmt.Printf("  security response rules: %d\n", s.securityResponseRules)
	fmt.Println()
	fmt.Printf("Open the console at %s\n", base)
}

// ---------------------------------------------------------------------------
// 1. Assets + dependency graph
// ---------------------------------------------------------------------------

type createAssetReq struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Criticality string `json:"criticality"`
}

type createAssetRelationshipReq struct {
	FromAssetID string `json:"from_asset_id"`
	ToAssetID   string `json:"to_asset_id"`
	Type        string `json:"type"`
}

// seedAssets registers the estate and its dependency graph, returning a
// name -> asset_id map every later domain looks up its asset by.
func seedAssets(ctx context.Context, c *apiClient, sum *summary) map[string]string {
	type spec struct{ name, typ, env, crit string }
	specs := []spec{
		{"web-frontend", "service", "production", "high"},
		{"api-gateway", "service", "production", "high"},
		{"primary-db", "database", "production", "critical"},
		{"auth-service", "service", "production", "critical"},
		{"cache-node", "cache", "staging", "low"},
		{"worker-queue", "service", "production", "medium"},
	}
	ids := make(map[string]string, len(specs))
	for _, sp := range specs {
		out, ok := c.post(ctx, "/v1/admin/assets", createAssetReq{
			Type: sp.typ, Name: sp.name, Environment: sp.env, Criticality: sp.crit,
		})
		if !ok {
			log.Printf("WARN  asset %q was skipped (409); anything that depends on it will be skipped too", sp.name)
			continue
		}
		ids[sp.name] = str(out, "asset_id")
		sum.assets++
	}

	type edge struct{ from, to, typ string }
	edges := []edge{
		{"web-frontend", "api-gateway", "depends_on"},
		{"api-gateway", "primary-db", "depends_on"},
		{"api-gateway", "auth-service", "depends_on"},
		{"api-gateway", "cache-node", "depends_on"},
		{"worker-queue", "primary-db", "depends_on"},
	}
	for _, e := range edges {
		from, to := ids[e.from], ids[e.to]
		if from == "" || to == "" {
			log.Printf("WARN  skipping relationship %s -> %s: an endpoint asset was not created", e.from, e.to)
			continue
		}
		if _, ok := c.post(ctx, "/v1/admin/assets/relationships", createAssetRelationshipReq{
			FromAssetID: from, ToAssetID: to, Type: e.typ,
		}); ok {
			sum.relationships++
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// 2. Tenant org + users + memberships
// ---------------------------------------------------------------------------

type createUserReq struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type patchStatusReq struct {
	RowVersion int64  `json:"row_version"`
	Status     string `json:"status"`
}

type grantMembershipReq struct {
	OrgID  string `json:"org_id"`
	UserID string `json:"user_id"`
}

// seedOrgAndUsers resolves the caller's own tenant organisation, then
// registers, activates and grants membership to the roster the on-call
// schedule, escalation policy and incident assignments below all draw from.
// The resulting user_id map is stashed on sum so downstream sections can
// read it without another return value threading through main.
func seedOrgAndUsers(ctx context.Context, c *apiClient, sum *summary) string {
	org := c.mustGet(ctx, "/v1/admin/tenant-org")
	orgID := str(org, "org_id")

	type person struct{ email, name string }
	roster := []person{
		{"priya.nair@example.com", "Priya Nair"},
		{"raj.shah@example.com", "Raj Shah"},
		{"mei.lin@example.com", "Mei Lin"},
	}
	sum.userIDs = make(map[string]string, len(roster))
	for _, p := range roster {
		out, ok := c.post(ctx, "/v1/admin/users", createUserReq{Email: p.email, DisplayName: p.name})
		if !ok {
			log.Printf("WARN  user %q was skipped (409); it will not appear in on-call/assignment", p.email)
			continue
		}
		sum.users++
		userID := str(out, "user_id")
		rowVersion := out["row_version"]

		// A user is minted "invited" and holds no access until activated
		// (ADR-IDENTITY-001 §8.3) — activate before granting membership so the
		// membership/on-call/assignment checks below ("active member") pass.
		activated := c.mustPatch(ctx, "/v1/admin/users/"+userID, patchStatusReq{
			RowVersion: toInt64(rowVersion), Status: "active",
		})
		_ = activated

		if _, ok := c.post(ctx, "/v1/admin/memberships", grantMembershipReq{OrgID: orgID, UserID: userID}); ok {
			sum.memberships++
		}
		sum.userIDs[p.name] = userID
	}
	return orgID
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// 3. Alert rules
// ---------------------------------------------------------------------------

type createAlertRuleReq struct {
	AssetID         string  `json:"asset_id"`
	Metric          string  `json:"metric"`
	Comparator      string  `json:"comparator"`
	Threshold       float64 `json:"threshold"`
	ForDurationSecs int     `json:"for_duration_seconds"`
	Severity        string  `json:"severity"`
	SymptomClass    string  `json:"symptom_class"`
}

func seedAlertRules(ctx context.Context, c *apiClient, sum *summary, assetID map[string]string) {
	type spec struct {
		asset, metric, comparator, severity, symptom string
		threshold                                    float64
		forSecs                                      int
	}
	specs := []spec{
		{"primary-db", "cpu_utilization", "gt", "critical", "resource", 90, 300},
		{"api-gateway", "request_latency_ms", "gt", "warning", "availability", 800, 180},
		{"worker-queue", "queue_depth", "gt", "info", "unspecified", 500, 300},
	}
	for _, sp := range specs {
		asset := assetID[sp.asset]
		if asset == "" {
			log.Printf("WARN  skipping alert rule on %q: asset was not created", sp.asset)
			continue
		}
		if _, ok := c.post(ctx, "/v1/admin/alert-rules", createAlertRuleReq{
			AssetID: asset, Metric: sp.metric, Comparator: sp.comparator, Threshold: sp.threshold,
			ForDurationSecs: sp.forSecs, Severity: sp.severity, SymptomClass: sp.symptom,
		}); ok {
			sum.alertRules++
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Incidents
// ---------------------------------------------------------------------------

type createIncidentReq struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Severity    string  `json:"severity"`
	AssetID     *string `json:"asset_id"`
}

func seedIncidents(
	ctx context.Context, c *apiClient, sum *summary, assetID, userID map[string]string,
) []string {
	type spec struct {
		title, desc, severity, asset string
		assignee                     string // roster name, or "" for unassigned
		transitions                  []string
	}
	specs := []spec{
		{
			title: "primary-db CPU sustained above threshold", desc: "cpu_utilization has been above 90% for 5+ minutes",
			severity: "critical", asset: "primary-db",
		},
		{
			title: "api-gateway latency degradation", desc: "p99 request latency above 800ms",
			severity: "high", asset: "api-gateway", assignee: "Raj Shah",
			transitions: []string{"acknowledged"},
		},
		{
			title: "cache-node memory pressure", desc: "memory_utilization trending toward exhaustion",
			severity: "medium", asset: "cache-node", assignee: "Mei Lin",
			transitions: []string{"acknowledged", "investigating"},
		},
	}
	ids := make([]string, 0, len(specs))
	for _, sp := range specs {
		asset := assetID[sp.asset]
		var assetPtr *string
		if asset != "" {
			assetPtr = &asset
		}
		out, ok := c.post(ctx, "/v1/admin/incidents", createIncidentReq{
			Title: sp.title, Description: sp.desc, Severity: sp.severity, AssetID: assetPtr,
		})
		if !ok {
			continue
		}
		sum.incidents++
		id := str(out, "incident_id")
		ids = append(ids, id)
		rowVersion := toInt64(out["row_version"])

		if sp.assignee != "" {
			if uid := userID[sp.assignee]; uid != "" {
				assigned := c.mustPost(ctx, "/v1/admin/incidents/"+id+"/assign", struct {
					RowVersion     int64  `json:"row_version"`
					AssigneeUserID string `json:"assignee_user_id"`
				}{RowVersion: rowVersion, AssigneeUserID: uid})
				rowVersion = toInt64(assigned["row_version"])
				sum.incidentAssign++
			}
		}
		for _, status := range sp.transitions {
			transitioned := c.mustPost(ctx, "/v1/admin/incidents/"+id+"/transition", struct {
				RowVersion int64  `json:"row_version"`
				Status     string `json:"status"`
			}{RowVersion: rowVersion, Status: status})
			rowVersion = toInt64(transitioned["row_version"])
			sum.incidentTransitions++
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// 5. On-call schedule + roster
// ---------------------------------------------------------------------------

type createOnCallScheduleReq struct {
	Name                   string    `json:"name"`
	HandoffIntervalSeconds int       `json:"handoff_interval_seconds"`
	RotationStartAt        time.Time `json:"rotation_start_at"`
}

type addOnCallParticipantReq struct {
	UserID string `json:"user_id"`
}

func seedOnCall(ctx context.Context, c *apiClient, sum *summary, userID map[string]string) string {
	out, ok := c.post(ctx, "/v1/admin/on-call-schedules", createOnCallScheduleReq{
		Name: "Primary On-Call", HandoffIntervalSeconds: 7 * 24 * 3600, RotationStartAt: time.Now(),
	})
	if !ok {
		return ""
	}
	sum.onCallSchedules++
	scheduleID := str(out, "schedule_id")

	for _, name := range []string{"Priya Nair", "Raj Shah", "Mei Lin"} {
		uid := userID[name]
		if uid == "" {
			continue
		}
		if _, ok := c.post(ctx, "/v1/admin/on-call-schedules/"+scheduleID+"/participants", addOnCallParticipantReq{
			UserID: uid,
		}); ok {
			sum.onCallParticipants++
		}
	}
	return scheduleID
}

// ---------------------------------------------------------------------------
// 6. Escalation policy + tier
// ---------------------------------------------------------------------------

type createEscalationPolicyReq struct {
	Name string `json:"name"`
}

type addEscalationTierReq struct {
	OnCallScheduleID string `json:"on_call_schedule_id"`
	WaitSeconds      int    `json:"wait_seconds"`
}

func seedEscalation(ctx context.Context, c *apiClient, sum *summary, scheduleID string) {
	if scheduleID == "" {
		log.Printf("WARN  skipping escalation policy: on-call schedule was not created")
		return
	}
	out, ok := c.post(ctx, "/v1/admin/escalation-policies", createEscalationPolicyReq{Name: "Default Escalation"})
	if !ok {
		return
	}
	sum.escalationPolicies++
	policyID := str(out, "policy_id")

	if _, ok := c.post(ctx, "/v1/admin/escalation-policies/"+policyID+"/tiers", addEscalationTierReq{
		OnCallScheduleID: scheduleID, WaitSeconds: 300,
	}); ok {
		sum.escalationTiers++
	}
}

// ---------------------------------------------------------------------------
// 7. Maintenance window
// ---------------------------------------------------------------------------

type createMaintenanceWindowReq struct {
	AssetID  string    `json:"asset_id"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Reason   string    `json:"reason"`
}

func seedMaintenanceWindow(ctx context.Context, c *apiClient, sum *summary, assetID map[string]string) {
	asset := assetID["cache-node"]
	if asset == "" {
		log.Printf("WARN  skipping maintenance window: cache-node was not created")
		return
	}
	now := time.Now()
	if _, ok := c.post(ctx, "/v1/admin/maintenance-windows", createMaintenanceWindowReq{
		AssetID: asset, StartsAt: now.Add(-30 * time.Minute), EndsAt: now.Add(2*time.Hour + 30*time.Minute),
		Reason: "Scheduled patching of the cache layer",
	}); ok {
		sum.maintenanceWindows++
	}
}

// ---------------------------------------------------------------------------
// 8. Vulnerability findings
// ---------------------------------------------------------------------------

type ingestVulnFindingReq struct {
	AssetID     string `json:"asset_id"`
	VulnID      string `json:"vuln_id"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Scanner     string `json:"scanner"`
	Description string `json:"description"`
}

func seedVulnFindings(ctx context.Context, c *apiClient, sum *summary, assetID map[string]string) {
	type spec struct{ asset, vulnID, title, severity, scanner, desc string }
	specs := []spec{
		{"primary-db", "CVE-2024-3094", "xz-utils liblzma supply-chain backdoor", "critical", "trivy",
			"Malicious code in liblzma allows SSH authentication bypass on affected builds."},
		{"api-gateway", "CVE-2024-6387", "OpenSSH regreSSHion signal-handler race condition (RCE)", "critical", "nessus",
			"Unauthenticated remote code execution via a signal-handler race in sshd."},
		{"primary-db", "CVE-2023-4911", "glibc Looney Tunables local privilege escalation", "high", "trivy",
			"Buffer overflow in the dynamic loader's processing of GLIBC_TUNABLES."},
		{"cache-node", "CVE-2024-31449", "Redis Lua library sandbox escape", "high", "trivy",
			"A crafted Lua script can trigger a stack buffer overflow leading to RCE."},
		{"api-gateway", "CVE-2022-40684", "Fortinet FortiOS/FortiProxy authentication bypass", "medium", "qualys",
			"Improper authentication allows an attacker to perform admin operations via crafted requests."},
		{"cache-node", "CVE-2021-32626", "Redis Lua script heap overflow", "low", "qualys",
			"Specially crafted Lua scripts can corrupt the heap and potentially execute code."},
	}
	var findings []ingestVulnFindingReq
	for _, sp := range specs {
		asset := assetID[sp.asset]
		if asset == "" {
			log.Printf("WARN  skipping vuln finding %s on %q: asset was not created", sp.vulnID, sp.asset)
			continue
		}
		findings = append(findings, ingestVulnFindingReq{
			AssetID: asset, VulnID: sp.vulnID, Title: sp.title, Severity: sp.severity,
			Scanner: sp.scanner, Description: sp.desc,
		})
	}
	if len(findings) == 0 {
		return
	}
	out := c.mustPost(ctx, "/v1/admin/vuln-findings", struct {
		Findings []ingestVulnFindingReq `json:"findings"`
	}{Findings: findings})
	sum.vulnFindings += requireAllAccepted(out, "vuln-findings")
}

// requireAllAccepted inspects a batch-ingest response's "results" array and
// fails loudly if any item was rejected — a rejection on this tool's own
// hand-built, spec-validated payload is a bug in the tool, not a tolerable
// outcome, so it must never be swallowed. Returns the number accepted.
func requireAllAccepted(resp map[string]any, what string) int {
	results, _ := resp["results"].([]any)
	accepted := 0
	for _, r := range results {
		item, _ := r.(map[string]any)
		if str(item, "outcome") == "accepted" {
			accepted++
			continue
		}
		log.Fatalf("%s: item %v rejected: %s", what, item["index"], str(item, "reason"))
	}
	return accepted
}

// ---------------------------------------------------------------------------
// 9. IOCs
// ---------------------------------------------------------------------------

type createIOCReq struct {
	IndicatorType  string `json:"indicator_type"`
	IndicatorValue string `json:"indicator_value"`
	Severity       string `json:"severity"`
	Description    string `json:"description"`
	Source         string `json:"source"`
}

const c2IP = "203.0.113.66"

func seedIOCs(ctx context.Context, c *apiClient, sum *summary) {
	specs := []createIOCReq{
		{IndicatorType: "ip", IndicatorValue: c2IP, Severity: "high",
			Description: "Known C2 callback address", Source: "threat-intel-feed"},
		{IndicatorType: "domain", IndicatorValue: "evil-exfil.example", Severity: "critical",
			Description: "Data-exfiltration domain observed in breach reporting", Source: "threat-intel-feed"},
		{IndicatorType: "file_hash", IndicatorValue: "44d88612fea8a8f36de82e1278abb02f", Severity: "medium",
			Description: "Malware dropper hash (MD5)", Source: "threat-intel-feed"},
	}
	for _, sp := range specs {
		if _, ok := c.post(ctx, "/v1/admin/iocs", sp); ok {
			sum.iocs++
		}
	}
}

// ---------------------------------------------------------------------------
// 10. Detection rule + security observations
// ---------------------------------------------------------------------------

type createSecurityRuleReq struct {
	AssetID          string `json:"asset_id"`
	ObservationType  string `json:"observation_type"`
	MinSeverity      string `json:"min_severity"`
	WindowSeconds    int    `json:"window_seconds"`
	ThresholdCount   int    `json:"threshold_count"`
	IncidentSeverity string `json:"incident_severity"`
}

type ingestSecurityObservationReq struct {
	AssetID         string            `json:"asset_id"`
	ObservationType string            `json:"observation_type"`
	Source          string            `json:"source"`
	Severity        string            `json:"severity"`
	ObservedAt      string            `json:"observed_at"`
	Attributes      map[string]string `json:"attributes"`
}

func seedSecurityRuleAndObservations(ctx context.Context, c *apiClient, sum *summary, assetID map[string]string) {
	asset := assetID["auth-service"]
	if asset == "" {
		log.Printf("WARN  skipping security rule/observations: auth-service was not created")
		return
	}
	if _, ok := c.post(ctx, "/v1/admin/security-rules", createSecurityRuleReq{
		AssetID: asset, ObservationType: "auth_failure", MinSeverity: "medium",
		WindowSeconds: 300, ThresholdCount: 5, IncidentSeverity: "high",
	}); ok {
		sum.securityRules++
	}

	now := time.Now()
	type spec struct {
		severity, srcIP, user string
		offset                time.Duration
	}
	specs := []spec{
		{"medium", "198.51.100.23", "jsmith", -10 * time.Minute},
		{"medium", "198.51.100.23", "jsmith", -9 * time.Minute},
		{"high", c2IP, "admin", -8 * time.Minute},
		{"high", c2IP, "admin", -7 * time.Minute},
		{"critical", c2IP, "admin", -6 * time.Minute},
		{"low", "192.0.2.15", "svc-backup", -5 * time.Minute},
	}
	observations := make([]ingestSecurityObservationReq, 0, len(specs))
	for _, sp := range specs {
		observations = append(observations, ingestSecurityObservationReq{
			AssetID: asset, ObservationType: "auth_failure", Source: "auth-service-logs",
			Severity: sp.severity, ObservedAt: rfc3339(now.Add(sp.offset)),
			Attributes: map[string]string{"src_ip": sp.srcIP, "user": sp.user},
		})
	}
	out := c.mustPost(ctx, "/v1/admin/security-observations", struct {
		Observations []ingestSecurityObservationReq `json:"observations"`
	}{Observations: observations})
	sum.securityObservations += requireAllAccepted(out, "security-observations")
}

// ---------------------------------------------------------------------------
// 11. Risk register
// ---------------------------------------------------------------------------

type createRiskReq struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
	Likelihood  string  `json:"likelihood"`
	Impact      string  `json:"impact"`
	Treatment   string  `json:"treatment"`
	AssetID     *string `json:"asset_id"`
}

func seedRisks(ctx context.Context, c *apiClient, sum *summary, assetID map[string]string) {
	type spec struct {
		title, desc, category, likelihood, impact, treatment, asset string
	}
	specs := []spec{
		{"Unpatched internet-facing database", "primary-db carries open critical CVEs and is reachable from api-gateway.",
			"vulnerability", "likely", "severe", "mitigate", "primary-db"},
		{"Stale offboarding process", "Departed contractors' memberships are not consistently revoked on schedule.",
			"process", "possible", "moderate", "mitigate", ""},
		{"Vendor SLA lapse", "The primary monitoring vendor's support contract lapses next quarter.",
			"vendor", "unlikely", "major", "accept", ""},
		{"Single on-call escalation point of failure", "The escalation policy has exactly one tier and one schedule.",
			"operational", "rare", "minor", "accept", ""},
	}
	for _, sp := range specs {
		var assetPtr *string
		if sp.asset != "" {
			if id := assetID[sp.asset]; id != "" {
				assetPtr = &id
			}
		}
		if _, ok := c.post(ctx, "/v1/admin/risks", createRiskReq{
			Title: sp.title, Description: sp.desc, Category: sp.category,
			Likelihood: sp.likelihood, Impact: sp.impact, Treatment: sp.treatment, AssetID: assetPtr,
		}); ok {
			sum.risks++
		}
	}
}

// ---------------------------------------------------------------------------
// 12. Compliance controls + evidence
// ---------------------------------------------------------------------------

type createComplianceControlReq struct {
	Framework   string `json:"framework"`
	ControlRef  string `json:"control_ref"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type addControlEvidenceReq struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func seedCompliance(ctx context.Context, c *apiClient, sum *summary) {
	type spec struct {
		ref, title, desc string
		// transitions, applied in order, from not_implemented.
		transitions []string
		evidence    []addControlEvidenceReq
	}
	specs := []spec{
		{"CC6.1", "Logical access controls",
			"Access to production systems is restricted to authorized personnel and reviewed periodically.",
			[]string{"in_progress"},
			[]addControlEvidenceReq{{Kind: "attestation", Value: "Access review scheduled for the next sprint; owner: Raj Shah."}},
		},
		{"CC7.2", "System monitoring",
			"Security events are monitored and evaluated to identify anomalies indicative of a security incident.",
			[]string{"in_progress", "implemented"},
			[]addControlEvidenceReq{{Kind: "url", Value: "https://monitoring.internal.example/dashboards/soc-overview"}},
		},
		{"CC8.1", "Change management",
			"Changes to infrastructure and software are authorized, tested, and approved before deployment.",
			nil, nil,
		},
	}
	for _, sp := range specs {
		out, ok := c.post(ctx, "/v1/admin/compliance-controls", createComplianceControlReq{
			Framework: "SOC2", ControlRef: sp.ref, Title: sp.title, Description: sp.desc,
		})
		if !ok {
			continue
		}
		sum.complianceControls++
		controlID := str(out, "control_id")
		rowVersion := toInt64(out["row_version"])

		for _, status := range sp.transitions {
			patched := c.mustPatch(ctx, "/v1/admin/compliance-controls/"+controlID, patchStatusReq{
				RowVersion: rowVersion, Status: status,
			})
			rowVersion = toInt64(patched["row_version"])
		}
		for _, ev := range sp.evidence {
			c.mustPost(ctx, "/v1/admin/compliance-controls/"+controlID+"/evidence", ev)
			sum.controlEvidence++
		}
	}
}

// ---------------------------------------------------------------------------
// 13. Security response rule
// ---------------------------------------------------------------------------

type createSecurityResponseRuleReq struct {
	Name         string          `json:"name"`
	MinSeverity  string          `json:"min_severity"`
	ActionType   string          `json:"action_type"`
	ActionConfig json.RawMessage `json:"action_config"`
}

func seedSecurityResponseRule(ctx context.Context, c *apiClient, sum *summary) {
	if _, ok := c.post(ctx, "/v1/admin/security-response-rules", createSecurityResponseRuleReq{
		Name: "Webhook SOAR on high severity", MinSeverity: "high", ActionType: "http",
		ActionConfig: json.RawMessage(`{"url":"https://soar.example.com/webhook","method":"POST"}`),
	}); ok {
		sum.securityResponseRules++
	}
}
