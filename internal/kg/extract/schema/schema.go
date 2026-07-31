// Package schema is extractor E3: the database schema, derived from migration
// SQL (§III).
//
// One node per table, column, index, constraint and trigger, and an `owns` edge
// from a table to each object declared against it. The migration corpus is the
// only source: nothing here connects to a database, so the graph describes what
// the repository says the schema is, which is the thing a reviewer can check.
//
// # Why this reads statements and not lines
//
// SQL statements are not lines. Three of the four declaration forms in this
// corpus routinely put the clause that names their table on a line after the
// keyword — 15 of 31 ALTER TABLE ... ADD, 13 of 43 CREATE INDEX, and all four
// CREATE TRIGGER. A line-oriented scanner sees the keyword without the clause
// and drops the object, silently: the graph still validates, the totals still
// look plausible, and the omission surfaces only as a wrong answer much later.
//
// So the corpus is split into statements first, at semicolons that are not
// inside a string, a comment or a dollar-quoted body, and each whole statement
// is matched. Positions survive the split because comments and dollar bodies
// are blanked in place rather than removed, which keeps every byte offset equal
// to the offset in the file and lets each object keep an exact line number.
//
// Blanking rather than deleting also settles the comment problem on its own. A
// migration comment here reads "CREATE TABLE in schemas that have no
// audit_event"; read as code it declares a table called "in". Prose that
// mentions DDL is not DDL.
//
// A dollar-quoted body is opaque by construction: one migration builds its
// triggers inside a DO block from a format string, so those statements do not
// exist as text at all. §III's rule for anything unparsed is warn and continue.
// E3 emits what it can read, never invents what it cannot, and never fails on
// a statement it does not model.
package schema

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
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

// Declaration forms E3 recognises. Each is anchored at the start of a whole
// statement, so a keyword appearing mid-expression is never a declaration and a
// clause on a later line is never lost.
var (
	reTable    = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	rePartOf   = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[a-z_][a-z0-9_]*\s+PARTITION\s+OF\b`)
	reIndex    = regexp.MustCompile(`(?is)^CREATE\s+(UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)\s+ON\s+([a-z_][a-z0-9_]*)`)
	reTrigger  = regexp.MustCompile(`(?is)^CREATE\s+(?:OR\s+REPLACE\s+)?(?:CONSTRAINT\s+)?TRIGGER\s+([a-z_][a-z0-9_]*)`)
	reAlterAdd = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([a-z_][a-z0-9_]*)\s+ADD\s+(COLUMN|CONSTRAINT)\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	reOnTarget = regexp.MustCompile(`(?is)\bON\s+([a-z_][a-z0-9_]*)`)

	reBodyConstraint = regexp.MustCompile(`(?is)^CONSTRAINT\s+([a-z_][a-z0-9_]*)`)
	reBodyColumn     = regexp.MustCompile(`(?is)^([a-z_][a-z0-9_]*)\s+\S`)
	reBodyClause     = regexp.MustCompile(`(?is)^(PRIMARY\s+KEY|UNIQUE|CHECK|FOREIGN\s+KEY|EXCLUDE|LIKE|PARTITION|DEFERRABLE)\b`)
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

	// Files are visited by name. Migration filenames are timestamped, so name
	// order is application order and an object is attributed to the migration
	// that first declares it.
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
		c.file(migrationDir+"/"+name, string(raw))
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

// file reads one migration.
func (c *collector) file(source, sql string) {
	code := blankNonCode(sql)
	lines := newLineIndex(code)
	for _, st := range statements(code) {
		c.statement(source, lines, st)
	}
}

// statement classifies one whole statement and records what it declares.
//
// A statement matching nothing here is skipped, not reported: §III makes an
// unmodelled statement a warning rather than a failure, and a schema extractor
// that aborted on a function body would block the entire graph.
func (c *collector) statement(source string, lines *lineIndex, st span) {
	text := st.text
	at := lines.at(st.off)

	if m := reTable.FindStringSubmatch(text); m != nil {
		table := strings.ToLower(m[1])
		c.add(kindTable, table, source, at, "", nil)
		// A partition inherits its parent's columns and declares none of its
		// own; its parenthesised group is a FOR VALUES clause, not a body.
		if !rePartOf.MatchString(text) {
			if body, off := balanced(text); body != "" {
				for _, item := range topLevelItems(body, st.off+off) {
					c.bodyItem(table, source, lines, item)
				}
			}
		}
		return
	}

	if m := reIndex.FindStringSubmatch(text); m != nil {
		var attrs map[string]string
		if m[1] != "" {
			attrs = map[string]string{"unique": "true"}
		}
		c.add(kindIndex, strings.ToLower(m[2]), source, at, strings.ToLower(m[3]), attrs)
		return
	}

	if m := reTrigger.FindStringSubmatch(text); m != nil {
		name := strings.ToLower(m[1])
		// The target is the first ON after the trigger's own name, so a trigger
		// whose name contains "on" cannot be read as its own target.
		rest := text[strings.Index(strings.ToLower(text), name)+len(name):]
		if om := reOnTarget.FindStringSubmatch(rest); om != nil {
			c.add(kindTrigger, name, source, at, strings.ToLower(om[1]), nil)
		}
		return
	}

	if m := reAlterAdd.FindStringSubmatch(text); m != nil {
		table, name := strings.ToLower(m[1]), strings.ToLower(m[3])
		kind := kindConstraint
		if strings.EqualFold(m[2], "COLUMN") {
			kind = kindColumn
		}
		c.add(kind, table+"."+name, source, at, table, nil)
	}
}

// bodyItem records one comma-separated item of a CREATE TABLE body.
func (c *collector) bodyItem(table, source string, lines *lineIndex, it span) {
	text := strings.TrimSpace(it.text)
	if text == "" {
		return
	}
	at := lines.at(it.off + (len(it.text) - len(strings.TrimLeft(it.text, " \t\r\n"))))
	if m := reBodyConstraint.FindStringSubmatch(text); m != nil {
		c.add(kindConstraint, table+"."+strings.ToLower(m[1]), source, at, table, nil)
		return
	}
	if reBodyClause.MatchString(text) {
		return // a table-level clause, not a named object
	}
	if m := reBodyColumn.FindStringSubmatch(text); m != nil {
		c.add(kindColumn, table+"."+strings.ToLower(m[1]), source, at, table, nil)
	}
}

// span is a piece of source together with its offset in the file.
type span struct {
	text string
	off  int
}

// blankNonCode replaces comment and dollar-quoted content with spaces, keeping
// every newline and every byte offset. String literals are left intact: a
// comment marker inside quotes is data.
//
// Blanking rather than deleting is what lets a statement keep an exact line
// number after the corpus has been stripped of prose.
func blankNonCode(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	blank := func(s string) {
		for _, r := range s {
			if r == '\n' {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		}
	}
	for i, n := 0, len(src); i < n; {
		switch {
		case src[i] == '\'':
			j := i + 1
			for j < n {
				if src[j] == '\'' {
					if j+1 < n && src[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j < n {
				j++
			}
			b.WriteString(src[i:j])
			i = j
		case src[i] == '-' && i+1 < n && src[i+1] == '-':
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				j = n
			} else {
				j += i
			}
			blank(src[i:j])
			i = j
		case src[i] == '/' && i+1 < n && src[i+1] == '*':
			j := strings.Index(src[i:], "*/")
			if j < 0 {
				j = n
			} else {
				j += i + 2
			}
			blank(src[i:j])
			i = j
		default:
			// Only a '$' can open a dollar quote. Without this guard the tag
			// scan runs at every byte of the corpus, which cost 906ms per
			// extraction against §III's 200ms budget.
			if tag := dollarTag(src, i); src[i] == '$' && tag != "" {
				j := strings.Index(src[i+len(tag):], tag)
				if j < 0 {
					j = n
				} else {
					j += i + len(tag) + len(tag)
				}
				blank(src[i:j])
				i = j
				continue
			}
			b.WriteByte(src[i])
			i++
		}
	}
	return b.String()
}

// dollarTag reports the dollar-quote delimiter opening at s[i], if any.
//
// Hand-rolled rather than a regex: this is consulted once per '$' in the
// corpus, and a compiled match here dominated the whole extraction.
func dollarTag(s string, i int) string {
	if i >= len(s) || s[i] != '$' {
		return ""
	}
	j := i + 1
	for j < len(s) && (s[j] == '_' ||
		(s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z') ||
		(j > i+1 && s[j] >= '0' && s[j] <= '9')) {
		j++
	}
	if j < len(s) && s[j] == '$' {
		return s[i : j+1]
	}
	return ""
}

// statements splits blanked source at semicolons that terminate a statement.
func statements(code string) []span {
	var out []span
	start := 0
	for i := 0; i < len(code); i++ {
		if code[i] == '\'' {
			j := i + 1
			for j < len(code) && code[j] != '\'' {
				j++
			}
			i = j
			continue
		}
		if code[i] == ';' {
			if t := strings.TrimSpace(code[start:i]); t != "" {
				out = append(out, trimmed(code[start:i], start))
			}
			start = i + 1
		}
	}
	if t := strings.TrimSpace(code[start:]); t != "" {
		out = append(out, trimmed(code[start:], start))
	}
	return out
}

// trimmed drops surrounding whitespace while keeping the offset accurate.
func trimmed(s string, off int) span {
	lead := len(s) - len(strings.TrimLeft(s, " \t\r\n"))
	return span{text: strings.TrimSpace(s), off: off + lead}
}

// balanced returns the first parenthesised group of s and its offset, or "".
func balanced(s string) (string, int) {
	open := strings.IndexByte(s, '(')
	if open < 0 {
		return "", 0
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], open + 1
			}
		}
	}
	return "", 0
}

// topLevelItems splits a body on commas that are not nested in parentheses.
func topLevelItems(body string, off int) []span {
	var out []span
	depth, start := 0, 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, span{text: body[start:i], off: off + start})
				start = i + 1
			}
		}
	}
	if start < len(body) {
		out = append(out, span{text: body[start:], off: off + start})
	}
	return out
}

// lineIndex converts a byte offset to a 1-based line number.
type lineIndex struct{ nl []int }

func newLineIndex(s string) *lineIndex {
	idx := &lineIndex{}
	for i, r := range s {
		if r == '\n' {
			idx.nl = append(idx.nl, i)
		}
	}
	return idx
}

func (l *lineIndex) at(off int) int {
	return sort.SearchInts(l.nl, off) + 1
}
