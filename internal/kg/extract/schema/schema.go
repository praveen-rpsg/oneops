// Package schema is extractor E3: the database schema, derived from migration
// SQL (§III).
//
// One node per table, column, index, constraint and trigger, and an `owns` edge
// from a table to each object declared against it. The migration corpus is the
// only source: nothing here connects to a database, so the graph describes what
// the repository says the schema is, which is the thing a reviewer can check.
//
// Comments are stripped before anything is matched. This is not tidiness — a
// migration comment in this corpus reads "CREATE TABLE in schemas that have no
// audit_event", and a parser that read comments would derive a table called
// "in". Prose that mentions DDL is not DDL.
//
// Dollar-quoted blocks are skipped whole. One migration builds triggers inside
// a DO block from a format string, so the statements do not exist as text and
// cannot be parsed. §III's rule for anything unparsed is warn and continue: E3
// emits what it can read and never fails on what it cannot, because a schema
// extractor that aborts on one dynamic statement would block the whole graph.
package schema

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/rpsg/oneops/internal/kg/graph"
	"github.com/rpsg/oneops/internal/kg/model"
)

// ExtractorID is this extractor's identity in §III's table.
const ExtractorID = "E3"

// migrationDir is where the corpus lives, relative to the repository root.
const migrationDir = "internal/store/migrate/sql"

const (
	kindTable      = "table"
	kindColumn     = "column"
	kindIndex      = "index"
	kindConstraint = "constraint"
	kindTrigger    = "trigger"
	edgeOwns       = "owns"
)

// Statement forms E3 recognises. Each is anchored at a statement start so a
// keyword appearing mid-expression cannot be mistaken for a declaration.
var (
	reCreateTable = regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	reCreateIndex = regexp.MustCompile(`(?i)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	reCreateTrig  = regexp.MustCompile(`(?i)^\s*CREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+([a-z_][a-z0-9_]*)`)
	reOnTarget    = regexp.MustCompile(`(?i)\bON\s+([a-z_][a-z0-9_]*)`)
	reAlterAdd    = regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?([a-z_][a-z0-9_]*)\s+ADD\s+(COLUMN|CONSTRAINT)\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	reInlineConst = regexp.MustCompile(`(?i)^\s*CONSTRAINT\s+([a-z_][a-z0-9_]*)`)
	reColumn      = regexp.MustCompile(`^\s*([a-z_][a-z0-9_]*)\s+[a-z]`)
	reUnique      = regexp.MustCompile(`(?i)\bUNIQUE\b`)

	// A line that starts a table-level clause rather than a column.
	reTableClause = regexp.MustCompile(`(?i)^\s*(PRIMARY\s+KEY|UNIQUE|CHECK|FOREIGN\s+KEY|EXCLUDE|LIKE|PARTITION)\b`)
)

// Extractor derives the schema. It holds no state and opens no connection.
type Extractor struct{}

// ID reports the extractor's identity (§III).
func (Extractor) ID() string { return ExtractorID }

// Extract reads the migration corpus and returns the schema it declares.
func (Extractor) Extract(_ context.Context, root string) ([]graph.Node, []graph.Edge, error) {
	dir := filepath.Join(root, migrationDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("schema: read %s: %w", migrationDir, err)
	}

	// Files are visited in name order. Migration filenames are timestamped, so
	// name order is application order, and an object is attributed to the
	// migration that first declares it.
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	slices.Sort(files)
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("schema: %s holds no migration", migrationDir)
	}

	c := &collector{seen: map[string]bool{}}
	for _, name := range files {
		raw, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			return nil, nil, fmt.Errorf("schema: read %s: %w", name, rerr)
		}
		// Amendment A1: the source is repository-relative, never the path this
		// process happened to read from.
		c.scan(migrationDir+"/"+name, string(raw))
	}

	slices.SortFunc(c.nodes, func(a, b graph.Node) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(c.edges, func(a, b graph.Edge) int {
		if x := strings.Compare(a.From, b.From); x != 0 {
			return x
		}
		return strings.Compare(a.To, b.To)
	})
	return c.nodes, c.edges, nil
}

// collector accumulates the objects a corpus declares, first declaration wins.
type collector struct {
	nodes []graph.Node
	edges []graph.Edge
	seen  map[string]bool
}

// add records a node unless its identity is already known. A table altered by a
// later migration is the same table, not a second one.
func (c *collector) add(kind, identity, source string, line int, owner string, attrs map[string]string) {
	id := kind + ":" + identity
	if c.seen[id] {
		return
	}
	c.seen[id] = true
	c.nodes = append(c.nodes, graph.Node{
		ID:         id,
		Kind:       kind,
		Attrs:      attrs,
		Evidence:   []graph.Evidence{{Source: source, Line: line, Rule: "E3." + kind}},
		Origin:     model.OriginDerived,
		Confidence: model.ConfidenceCertain,
	})
	if owner != "" {
		c.edges = append(c.edges, graph.Edge{
			From: kindTable + ":" + owner, To: id, Kind: edgeOwns,
			Evidence:   []graph.Evidence{{Source: source, Line: line, Rule: "E3.owns"}},
			Origin:     model.OriginDerived,
			Confidence: model.ConfidenceCertain,
		})
	}
}

// pending is a declaration whose owning table has not been read yet.
type pending struct {
	kind, name, source string
	line               int
	attrs              map[string]string
}

// open records a declaration, emitting it at once when the ON clause is already
// present and otherwise returning it to be resolved by a following line.
func (c *collector) open(kind, name, source string, line int, text string, attrs map[string]string) *pending {
	p := &pending{kind: kind, name: strings.ToLower(name), source: source, line: line, attrs: attrs}
	// Match the ON clause only after the declared name, so an index whose own
	// name contains "on" cannot be read as its own target.
	rest := text[strings.Index(strings.ToLower(text), p.name)+len(p.name):]
	if m := reOnTarget.FindStringSubmatch(rest); m != nil {
		c.add(p.kind, p.name, source, line, strings.ToLower(m[1]), p.attrs)
		return nil
	}
	return p
}

// scan reads one migration file.
func (c *collector) scan(source, sql string) {
	lines := strings.Split(sql, "\n")
	inDollar := false
	table := "" // the table whose CREATE body is open
	depth := 0  // parenthesis depth inside that body

	// An index or trigger names its table in an ON clause that may sit on the
	// declaring line or on a following one. Ten of this corpus's indexes put it
	// on the next line, so a single-line match silently loses a quarter of them.
	var pend *pending

	for i, raw := range lines {
		line := stripComment(raw)
		n := i + 1

		// A dollar-quoted body is opaque: its statements may be built at run
		// time and do not exist as text to parse.
		//
		// Counted rather than tested for presence. A function written on one
		// line carries the delimiter twice, and toggling once for it would
		// leave the scanner inside a block that had already closed — swallowing
		// every declaration in the rest of the file.
		if q := strings.Count(line, "$$"); q > 0 {
			if q%2 == 1 {
				inDollar = !inDollar
			}
			continue
		}
		if inDollar || strings.TrimSpace(line) == "" {
			continue
		}

		// Resolve an open declaration before considering a new statement.
		if pend != nil {
			if m := reOnTarget.FindStringSubmatch(line); m != nil {
				c.add(pend.kind, pend.name, source, pend.line, strings.ToLower(m[1]), pend.attrs)
				pend = nil
			}
			continue
		}

		if table != "" {
			depth += strings.Count(line, "(") - strings.Count(line, ")")
			c.bodyLine(table, source, n, line)
			if depth <= 0 {
				table, depth = "", 0
			}
			continue
		}

		switch {
		case reCreateTable.MatchString(line):
			m := reCreateTable.FindStringSubmatch(line)
			name := strings.ToLower(m[1])
			c.add(kindTable, name, source, n, "", nil)
			// A partition declaration has no column body of its own.
			if strings.Contains(line, "(") {
				table = name
				depth = strings.Count(line, "(") - strings.Count(line, ")")
			}

		case reCreateIndex.MatchString(line):
			var attrs map[string]string
			if reUnique.MatchString(line) {
				attrs = map[string]string{"unique": "true"}
			}
			pend = c.open(kindIndex, reCreateIndex.FindStringSubmatch(line)[1], source, n, line, attrs)

		case reCreateTrig.MatchString(line):
			pend = c.open(kindTrigger, reCreateTrig.FindStringSubmatch(line)[1], source, n, line, nil)

		case reAlterAdd.MatchString(line):
			m := reAlterAdd.FindStringSubmatch(line)
			owner := strings.ToLower(m[1])
			name := strings.ToLower(m[3])
			if strings.EqualFold(m[2], "COLUMN") {
				c.add(kindColumn, owner+"."+name, source, n, owner, nil)
			} else {
				c.add(kindConstraint, owner+"."+name, source, n, owner, nil)
			}
		}
	}
}

// bodyLine reads one line of an open CREATE TABLE body.
func (c *collector) bodyLine(table, source string, line int, text string) {
	if m := reInlineConst.FindStringSubmatch(text); m != nil {
		c.add(kindConstraint, table+"."+strings.ToLower(m[1]), source, line, table, nil)
		return
	}
	if reTableClause.MatchString(text) {
		return
	}
	if m := reColumn.FindStringSubmatch(text); m != nil {
		c.add(kindColumn, table+"."+strings.ToLower(m[1]), source, line, table, nil)
	}
}

// stripComment removes a line comment, leaving anything inside a string literal
// alone. A comment marker inside quotes is data, not a comment.
func stripComment(line string) string {
	quoted := false
	for i := 0; i+1 < len(line); i++ {
		if line[i] == '\'' {
			quoted = !quoted
		}
		if !quoted && line[i] == '-' && line[i+1] == '-' {
			return line[:i]
		}
	}
	return line
}
