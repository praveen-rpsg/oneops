// Package storage reads and writes pkg.json.
//
// The file is generated, never committed (ADR-PKG-001): CI regenerates it on
// every run and publishes it as a build artifact, so nothing here treats an
// existing file as authority. What this package owes its callers is that the
// bytes are a faithful, byte-stable rendering of a Graph — two runs over one
// tree must produce identical files, because that identity is the whole basis
// on which the graph is trusted without being stored.
//
// It does not validate graph invariants. A file can be perfectly well-formed
// storage and a badly-formed graph; separating the two means a caller can tell
// "this file is corrupt" from "this graph is wrong". Call graph.Validate for
// the second question.
package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/rpsg/oneops/internal/kg/graph"
)

// Filename is the generated artifact's name at the repository root (§VII).
const Filename = "pkg.json"

// ErrUnsupportedSchemaVersion is a graph this build cannot read or write.
//
// §II gives storage the job of refusing a newer version: a graph written by a
// later build may use fields this one would silently drop, and dropping them
// turns a load-then-save into data loss. A version below 1 is refused for the
// same reason in the other direction — it is either corrupt or predates the
// schema, and no build ever emitted one.
var ErrUnsupportedSchemaVersion = errors.New("storage: unsupported schema version")

// Encode renders a graph as the canonical pkg.json bytes.
//
// Two spaces of indentation and one trailing newline (§VII). Field order is the
// declaration order of §II, and Go sorts the keys of the one map in the model —
// a Node's Attrs — so nothing in the output depends on iteration order. The
// result is byte-identical for equal graphs, which is the property the whole
// regeneration model rests on.
func Encode(g *graph.Graph) ([]byte, error) {
	if err := supported(g.SchemaVersion); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("storage: encode: %w", err)
	}
	return append(out, '\n'), nil
}

// Decode parses pkg.json bytes back into a graph.
//
// The schema version is read first, so a file from a future build is refused
// with a version error rather than a confusing complaint about an unknown
// field. Unknown fields are then rejected: within a supported version they mean
// the file was written by something other than this package, and silently
// discarding them would make a load-then-save lose data it never reported.
func Decode(data []byte) (*graph.Graph, error) {
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("storage: %s is not valid JSON: %w", Filename, err)
	}
	if err := supported(probe.SchemaVersion); err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var g graph.Graph
	if err := dec.Decode(&g); err != nil {
		return nil, fmt.Errorf("storage: decode %s: %w", Filename, err)
	}
	// One JSON value per file. Anything after it means the file is truncated
	// mid-rewrite, concatenated, or not ours.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("storage: %s has trailing content after the graph", Filename)
	}
	return &g, nil
}

// ReadFile loads a graph from path.
func ReadFile(path string) (*graph.Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("storage: read %s: %w", path, err)
	}
	return Decode(data)
}

// WriteFile writes a graph to path in canonical form.
func WriteFile(path string, g *graph.Graph) error {
	data, err := Encode(g)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("storage: write %s: %w", path, err)
	}
	return nil
}

// supported reports whether this build can faithfully carry the given version.
func supported(v int) error {
	if v < 1 || v > graph.SchemaVersion {
		return fmt.Errorf("%w: have %d, this build carries 1..%d",
			ErrUnsupportedSchemaVersion, v, graph.SchemaVersion)
	}
	return nil
}
