package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrEmptyCanonicalInput is returned when Canonicalize is given no JSON value.
var ErrEmptyCanonicalInput = errors.New("audit: payload must not be empty")

// ErrTrailingCanonicalInput is returned when Canonicalize is given more than one
// JSON value (e.g. "{}{}" or "1 2"): an audit payload is exactly one document.
var ErrTrailingCanonicalInput = errors.New("audit: payload must be a single JSON value")

// Canonicalize returns the deterministic canonical byte encoding of a JSON
// payload — the single, stable representation that is hashed into the audit
// chain (ECR-01). It is the authoritative canonicalizer for the Audit Integrity
// capability: no other package (in particular, no governance or business caller)
// may canonicalize audit payloads.
//
// The canonical form is structural canonical JSON:
//   - object member keys are sorted lexicographically by UTF-8 code unit;
//   - all insignificant whitespace is removed (compact output);
//   - number literals are preserved exactly as written (via json.Number), so no
//     precision is lost and equal inputs map to equal bytes;
//   - '<', '>', and '&' are not escaped, so the result is the literal JSON text.
//
// Canonicalize is pure and deterministic: semantically identical JSON — whatever
// its key order or insignificant whitespace — yields identical bytes, and any
// semantic change yields different bytes. It never hashes, persists, sequences,
// or derives identifiers; producing the canonical bytes is its only concern.
func Canonicalize(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, ErrEmptyCanonicalInput
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserve number literals exactly; never widen to float64.
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("audit: canonicalize: %w", err)
	}
	if dec.More() {
		return nil, ErrTrailingCanonicalInput
	}

	// json.Marshal emits map keys in sorted order and json.Number verbatim; the
	// encoder lets us suppress HTML escaping so the bytes are the literal JSON.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("audit: canonicalize: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
