package compliance

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
)

// BuildJSON renders an evidence bundle as indented JSON. It is deterministic for
// a given bundle (fields are stably ordered by the struct definition, and slices
// are already deterministically ordered by the builder).
func BuildJSON(ev Evidence) ([]byte, error) {
	return json.MarshalIndent(ev, "", "  ")
}

// BuildZIP renders an evidence bundle as a ZIP containing a single evidence JSON
// file. The archive is reproducible: the entry's modified time is pinned to the
// bundle's GeneratedAt, so identical bundles yield byte-identical archives.
func BuildZIP(ev Evidence) ([]byte, error) {
	payload, err := BuildJSON(ev)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{
		Name:     fmt.Sprintf("evidence-%s.json", ev.GovernanceID),
		Method:   zip.Deflate,
		Modified: ev.GeneratedAt,
	}
	f, err := zw.CreateHeader(hdr)
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	if _, err := f.Write(payload); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
