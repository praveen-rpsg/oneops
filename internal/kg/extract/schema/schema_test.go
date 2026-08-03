package schema

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/kg/graph"
	"github.com/rpsg/oneops/internal/kg/model"
)

const repoRoot = "../../../.."

func extract(t *testing.T) ([]graph.Node, []graph.Edge) {
	t.Helper()
	n, e, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return n, e
}

func byKind(nodes []graph.Node) map[string][]graph.Node {
	out := map[string][]graph.Node{}
	for _, n := range nodes {
		out[n.Kind] = append(out[n.Kind], n)
	}
	return out
}

// ---------------------------------------------------------------- positive

func TestExtractorID(t *testing.T) {
	if got := (Extractor{}).ID(); got != "E3" {
		t.Errorf("ID() = %q, want %q", got, "E3")
	}
}

// The acceptance criterion: every table the migrations create is a node.
func TestEveryTableIsANode(t *testing.T) {
	nodes, _ := extract(t)
	got := map[string]bool{}
	for _, n := range byKind(nodes)[kindTable] {
		got[strings.TrimPrefix(n.ID, kindTable+":")] = true
	}
	// Named rather than derived: a second derivation sharing this one's regexes
	// would agree with it about anything, including a mistake.
	want := []string{
		"admin_audit_chain_head", "admin_audit_event", "app_user", "approval_record", "artifact_version",
		"asset", "asset_change_history", "asset_relationship",
		"audit_chain_head", "audit_event", "audit_event_default", "configuration_metadata",
		"configuration_object", "dependency_edge", "idempotency_key", "invitation",
		"membership", "notification", "organization", "policy", "policy_cursor", "policy_execution",
		"setting", "team", "team_membership", "telemetry_sample", "tenant", "webhook", "webhook_cursor",
		"webhook_delivery", "webhook_replay_job",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("table %q is created by a migration but is not a node", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extracted %d tables, want %d: %v", len(got), len(want), got)
	}
}

// Prose that mentions DDL is not DDL.
//
// A migration comment reads "CREATE TABLE in schemas that have no audit_event".
// A parser that did not strip comments would derive a table called "in" — the
// same defect that already exists in the S0.2 guard's own derivation.
func TestCommentsDoNotDeclareObjects(t *testing.T) {
	nodes, _ := extract(t)
	for _, n := range nodes {
		switch n.ID {
		case "table:in", "table:the", "table:not", "table:exists", "table:schemas":
			t.Errorf("%q was derived from prose; comments must be stripped before matching", n.ID)
		}
	}
	// The comment that would produce it is still in the corpus, so the test is
	// exercising the stripper rather than an absence.
	raw, err := os.ReadFile(filepath.Join(repoRoot,
		"internal/store/migrate/sql/20260728000002_audit_partition_truncate_guard.sql"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "CREATE TABLE in schemas") {
		t.Skip("the prose this guards against has been edited out of the corpus")
	}
}

// A quarter of this corpus's indexes put the ON clause on the line after the
// name. A single-line match silently lost ten of forty.
func TestIndexOnClauseMaySpanLines(t *testing.T) {
	nodes, edges := extract(t)
	idx := byKind(nodes)[kindIndex]
	if len(idx) != 66 {
		t.Errorf("extracted %d indexes, want 66", len(idx))
	}
	// ix_webhook_delivery_due declares ON on a following line.
	const want = "index:ix_webhook_delivery_due"
	var owner string
	for _, e := range edges {
		if e.To == want && e.Kind == edgeOwns {
			owner = e.From
		}
	}
	if owner != "table:webhook_delivery" {
		t.Errorf("%s is owned by %q, want table:webhook_delivery — the ON clause on the "+
			"following line was not resolved", want, owner)
	}
}

func TestUniqueIndexIsMarked(t *testing.T) {
	nodes, _ := extract(t)
	unique := 0
	for _, n := range byKind(nodes)[kindIndex] {
		if n.Attrs["unique"] == "true" {
			unique++
		}
	}
	if unique == 0 {
		t.Error("no index is marked unique, but the corpus declares CREATE UNIQUE INDEX")
	}
}

// A table's columns and constraints, checked against DDL a reader can open.
func TestTableShapeIsExact(t *testing.T) {
	nodes, edges := extract(t)
	var cols, cons []string
	for _, n := range nodes {
		switch {
		case strings.HasPrefix(n.ID, "column:app_user."):
			cols = append(cols, strings.TrimPrefix(n.ID, "column:app_user."))
		case strings.HasPrefix(n.ID, "constraint:app_user."):
			cons = append(cons, strings.TrimPrefix(n.ID, "constraint:app_user."))
		}
	}
	wantCols := []string{"created_at", "display_name", "email", "row_version", "status", "updated_at", "user_id"}
	if !reflect.DeepEqual(cols, wantCols) {
		t.Errorf("app_user columns:\ngot:  %v\nwant: %v", cols, wantCols)
	}
	wantCons := []string{"ck_user_status", "uq_user_email"}
	if !reflect.DeepEqual(cons, wantCons) {
		t.Errorf("app_user constraints:\ngot:  %v\nwant: %v", cons, wantCons)
	}
	owned := 0
	for _, e := range edges {
		if e.From == "table:app_user" {
			owned++
		}
	}
	if owned != len(wantCols)+len(wantCons) {
		t.Errorf("table:app_user owns %d objects, want %d", owned, len(wantCols)+len(wantCons))
	}
}

// COMMENT ON is a statement about an object, not a declaration of one.
func TestCommentOnDoesNotDeclareAColumn(t *testing.T) {
	nodes, _ := extract(t)
	for _, n := range nodes {
		if n.ID == "column:app_user.comment" || n.ID == "table:comment" {
			t.Errorf("%q was derived from a COMMENT ON statement", n.ID)
		}
	}
}

func TestTriggersAreOwnedByTheirTable(t *testing.T) {
	nodes, edges := extract(t)
	trig := byKind(nodes)[kindTrigger]
	if len(trig) != 6 {
		t.Errorf("extracted %d triggers, want 6: %v", len(trig), trig)
	}
	for _, tr := range trig {
		found := false
		for _, e := range edges {
			if e.To == tr.ID && e.Kind == edgeOwns && strings.HasPrefix(e.From, "table:") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is owned by no table", tr.ID)
		}
	}
}

// E3's output must satisfy §II on its own, since the pipeline validates the
// merged graph and a defect here would surface there with no author.
func TestOutputIsAValidGraph(t *testing.T) {
	nodes, edges := extract(t)
	g := &graph.Graph{SchemaVersion: graph.SchemaVersion, Nodes: nodes, Edges: edges}
	if err := g.Validate(); err != nil {
		t.Fatalf("E3's output does not satisfy §II: %v", err)
	}
	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.ID] = true
	}
	for _, e := range edges {
		if !ids[e.From] || !ids[e.To] {
			t.Errorf("edge %s->%s has an endpoint outside the graph", e.From, e.To)
		}
	}
	t.Logf("nodes=%d edges=%d", len(nodes), len(edges))
}

// Amendment A1: every source repository-relative, every record locatable.
func TestEvidenceIsAnchoredAndRelative(t *testing.T) {
	nodes, edges := extract(t)
	check := func(where string, ev []graph.Evidence) {
		if len(ev) == 0 {
			t.Errorf("%s carries no evidence", where)
			return
		}
		for _, e := range ev {
			switch {
			case strings.HasPrefix(e.Source, "/"), strings.HasPrefix(e.Source, "./"),
				strings.Contains(e.Source, `\`), strings.Contains(e.Source, ".."):
				t.Errorf("%s has a non-canonical source %q (A1)", where, e.Source)
			case !strings.HasPrefix(e.Source, "internal/store/migrate/sql/"):
				t.Errorf("%s is sourced outside the migration corpus: %q", where, e.Source)
			}
			if e.Line <= 0 {
				t.Errorf("%s evidence has line %d; a schema object is locatable", where, e.Line)
			}
			if !strings.HasPrefix(e.Rule, "E3.") {
				t.Errorf("%s evidence rule %q is not E3's", where, e.Rule)
			}
		}
	}
	for _, n := range nodes {
		check("node "+n.ID, n.Evidence)
		if n.Origin != model.OriginDerived || n.Confidence != model.ConfidenceCertain {
			t.Errorf("node %s: migration SQL is an executable artifact, want derived/certain", n.ID)
		}
	}
	for _, e := range edges {
		check("edge "+e.From+"->"+e.To, e.Evidence)
	}
}

func TestExtractionIsDeterministic(t *testing.T) {
	firstN, firstE := extract(t)
	want, err := json.Marshal(struct {
		N []graph.Node
		E []graph.Edge
	}{firstN, firstE})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := 0; i < 3; i++ {
		n, e := extract(t)
		got, merr := json.Marshal(struct {
			N []graph.Node
			E []graph.Edge
		}{n, e})
		if merr != nil {
			t.Fatalf("marshal %d: %v", i, merr)
		}
		if string(got) != string(want) {
			t.Fatalf("run %d differs", i)
		}
	}
}

// ------------------------------------------------- class-elimination guards

// A census of every kind, not of the three that happened to be right.
//
// The suite this replaces asserted totals for tables, indexes and triggers and
// spot-checked one table's columns. That table declares everything inline, so
// 12 columns and 3 constraints declared by multi-line ALTER TABLE were missing
// while every assertion passed.
func TestCorpusCensus(t *testing.T) {
	nodes, _ := extract(t)
	got := map[string]int{}
	for _, n := range nodes {
		got[n.Kind]++
	}
	want := map[string]int{
		kindTable: 31, kindColumn: 270, kindIndex: 66, kindConstraint: 81, kindTrigger: 6,
	}
	for kind, w := range want {
		if got[kind] != w {
			t.Errorf("%s: extracted %d, want %d", kind, got[kind], w)
		}
	}
	for kind := range got {
		if _, named := want[kind]; !named {
			t.Errorf("unexpected node kind %q — the census must cover every kind E3 emits", kind)
		}
	}
}

// The invariant that retires the defect class.
//
// Declarations are found by flattening whitespace and matching the flattened
// text — a technique that cannot care where a line break falls, and that shares
// no code with the extractor's statement splitter. Every declaration it finds
// must be a node. A future extractor change that loses a statement form fails
// here regardless of which form it is, so the class cannot come back one
// keyword at a time.
func TestEveryDeclarationResolvesToANode(t *testing.T) {
	nodes, _ := extract(t)
	have := map[string]bool{}
	for _, n := range nodes {
		have[n.ID] = true
	}

	dir := filepath.Join(repoRoot, migrationDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var (
		flatTable = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?([a-z_][a-z0-9_]*)`)
		flatIndex = regexp.MustCompile(`(?i)CREATE (?:UNIQUE )?INDEX (?:CONCURRENTLY )?(?:IF NOT EXISTS )?([a-z_][a-z0-9_]*) ON `)
		flatTrig  = regexp.MustCompile(`(?i)CREATE (?:OR REPLACE )?TRIGGER ([a-z_][a-z0-9_]*)`)
		flatAlter = regexp.MustCompile(`(?i)ALTER TABLE (?:IF EXISTS )?(?:ONLY )?([a-z_][a-z0-9_]*) ADD (COLUMN|CONSTRAINT) (?:IF NOT EXISTS )?([a-z_][a-z0-9_]*)`)
		collapse  = regexp.MustCompile(`\s+`)
		checked   int
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		// Blank prose and dynamic bodies first; a comment is not a declaration.
		flat := collapse.ReplaceAllString(blankNonCode(string(raw)), " ")

		for _, m := range flatTable.FindAllStringSubmatch(flat, -1) {
			checked++
			if id := "table:" + strings.ToLower(m[1]); !have[id] {
				t.Errorf("%s declares %s but it is not a node", e.Name(), id)
			}
		}
		for _, m := range flatIndex.FindAllStringSubmatch(flat, -1) {
			checked++
			if id := "index:" + strings.ToLower(m[1]); !have[id] {
				t.Errorf("%s declares %s but it is not a node", e.Name(), id)
			}
		}
		for _, m := range flatTrig.FindAllStringSubmatch(flat, -1) {
			checked++
			if id := "trigger:" + strings.ToLower(m[1]); !have[id] {
				t.Errorf("%s declares %s but it is not a node", e.Name(), id)
			}
		}
		for _, m := range flatAlter.FindAllStringSubmatch(flat, -1) {
			checked++
			kind := kindConstraint
			if strings.EqualFold(m[2], "COLUMN") {
				kind = kindColumn
			}
			id := kind + ":" + strings.ToLower(m[1]) + "." + strings.ToLower(m[3])
			if !have[id] {
				t.Errorf("%s declares %s but it is not a node", e.Name(), id)
			}
		}
	}
	// Anti-vacuity: a sweep that finds nothing passes forever.
	if checked < 50 {
		t.Fatalf("swept only %d declarations; the corpus scan is broken", checked)
	}
	t.Logf("declarations swept: %d", checked)
}

// The named regression for the defect this replaced.
//
// Fifteen of thirty-one ALTER TABLE ... ADD statements put the ADD clause on a
// line after the keyword. Twelve of them add the tenancy discriminator, so the
// graph claimed four tables were tenant-scoped when sixteen are.
func TestMultiLineAlterTableIsExtracted(t *testing.T) {
	nodes, edges := extract(t)
	have := map[string]bool{}
	for _, n := range nodes {
		have[n.ID] = true
	}
	for _, want := range []string{
		"column:artifact_version.tenant_id", "column:audit_chain_head.tenant_id",
		"column:audit_event.tenant_id", "column:configuration_metadata.tenant_id",
		"column:configuration_object.tenant_id", "column:dependency_edge.tenant_id",
		"column:idempotency_key.tenant_id", "column:policy.tenant_id",
		"column:policy_execution.tenant_id", "column:webhook.tenant_id",
		"column:webhook_delivery.tenant_id", "column:webhook_replay_job.tenant_id",
		"constraint:admin_audit_event.ck_admin_audit_subject_bound",
		"constraint:configuration_object.uq_cfg_tenant_artifact_version",
		"constraint:idempotency_key.idempotency_key_pkey",
	} {
		if !have[want] {
			t.Errorf("%s is declared by a multi-line ALTER TABLE but is not a node", want)
		}
	}

	// Twenty-five tables carry the discriminator (E2.1 adds
	// telemetry_sample). Anything less is a wrong answer to the question the
	// graph exists to answer.
	scoped := 0
	for _, n := range nodes {
		if strings.HasSuffix(n.ID, ".tenant_id") && n.Kind == kindColumn {
			scoped++
		}
	}
	if scoped != 25 {
		t.Errorf("%d tables carry tenant_id, want 25", scoped)
	}

	// A multi-line declaration must still be owned by its table.
	var owner string
	for _, e := range edges {
		if e.To == "column:artifact_version.tenant_id" && e.Kind == edgeOwns {
			owner = e.From
		}
	}
	if owner != "table:artifact_version" {
		t.Errorf("ownership lost for a multi-line declaration: owner=%q", owner)
	}
}

// ---------------------------------------------------------------- negative

// §III: an unparsed statement warns and continues. One migration builds its
// triggers inside a DO block from a format string, so those statements do not
// exist as text — and extraction must still succeed.
func TestUnparsedStatementsDoNotFail(t *testing.T) {
	dir := t.TempDir()
	sqlDir := filepath.Join(dir, migrationDir)
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const corpus = `
CREATE TABLE IF NOT EXISTS good_table (
    id text PRIMARY KEY
);

DO $$
BEGIN
    EXECUTE format('CREATE TABLE %s (id text)', 'dynamic_table');
    EXECUTE format('CREATE INDEX %s ON %s (id)', 'dyn_idx', 'dynamic_table');
END;
$$;

CREATE OR REPLACE FUNCTION some_fn() RETURNS trigger AS $$ BEGIN RETURN NULL; END; $$ LANGUAGE plpgsql;

CREATE POLICY p_something ON good_table USING (true);

THIS IS NOT SQL AT ALL ;;;

CREATE TABLE IF NOT EXISTS after_the_mess (
    id text PRIMARY KEY
);
`
	if err := os.WriteFile(filepath.Join(sqlDir, "0001_x.sql"), []byte(corpus), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	nodes, _, err := (Extractor{}).Extract(context.Background(), dir)
	if err != nil {
		t.Fatalf("unparsed input must warn and continue, not fail: %v", err)
	}
	got := map[string]bool{}
	for _, n := range nodes {
		got[n.ID] = true
	}
	if !got["table:good_table"] {
		t.Error("the statement before the unparsable block was lost")
	}
	if !got["table:after_the_mess"] {
		t.Error("extraction stopped at the unparsable statement instead of continuing")
	}
	// Statements built at run time are not text and must not be invented.
	if got["table:dynamic_table"] || got["index:dyn_idx"] {
		t.Error("an object was derived from inside a dollar-quoted block")
	}
}

func TestMissingCorpusIsAnError(t *testing.T) {
	if _, _, err := (Extractor{}).Extract(context.Background(), t.TempDir()); err == nil {
		t.Error("a root with no migration directory returned no error")
	}
}

func TestEmptyCorpusIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, migrationDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A corpus with no migrations means the derivation found nothing to read,
	// which is a broken root rather than a schema-less platform.
	if _, _, err := (Extractor{}).Extract(context.Background(), dir); err == nil {
		t.Error("an empty migration directory returned no error")
	}
}

func TestBlankNonCode(t *testing.T) {
	cases := map[string]string{
		"CREATE TABLE x (":             "CREATE TABLE x (",
		"-- CREATE TABLE in schemas":   "                          ",
		"CREATE TABLE x ( -- trailing": "CREATE TABLE x (            ",
		"DEFAULT 'a--b'":               "DEFAULT 'a--b'",
		"CHECK (s IN ('x')) -- note":   "CHECK (s IN ('x'))        ",
		"/* block */ SELECT":           "            SELECT",
	}
	for in, want := range cases {
		got := blankNonCode(in)
		if got != want {
			t.Errorf("blankNonCode(%q)\n got  %q\n want %q", in, got, want)
		}
		if len(got) != len(in) {
			t.Errorf("blankNonCode(%q) changed length %d -> %d; offsets would shift", in, len(in), len(got))
		}
	}
	// A dollar body is blanked but its newlines survive, so line numbers after
	// it stay correct.
	src := "A\n$$\nx\ny\n$$\nB"
	got := blankNonCode(src)
	if len(got) != len(src) || strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Errorf("dollar blanking changed geometry: %q", got)
	}
	if strings.Contains(got, "x") || strings.Contains(got, "y") {
		t.Errorf("dollar body survived blanking: %q", got)
	}
}
