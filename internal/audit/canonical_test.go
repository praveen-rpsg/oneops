package audit

import (
	"bytes"
	"errors"
	"testing"
)

func TestCanonicalize_KeyOrderIndependent(t *testing.T) {
	a, err := Canonicalize([]byte(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatalf("Canonicalize a: %v", err)
	}
	b, err := Canonicalize([]byte(`{"a":2,"b":1}`))
	if err != nil {
		t.Fatalf("Canonicalize b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("key order changed canonical bytes: %q vs %q", a, b)
	}
	if want := `{"a":2,"b":1}`; string(a) != want {
		t.Fatalf("canonical form = %q, want %q", a, want)
	}
}

func TestCanonicalize_WhitespaceIndependent(t *testing.T) {
	compact, err := Canonicalize([]byte(`{"a":1,"b":[2,3]}`))
	if err != nil {
		t.Fatalf("Canonicalize compact: %v", err)
	}
	spaced, err := Canonicalize([]byte("  {\n  \"a\" : 1 ,\n  \"b\" : [ 2 , 3 ]\n}\n"))
	if err != nil {
		t.Fatalf("Canonicalize spaced: %v", err)
	}
	if !bytes.Equal(compact, spaced) {
		t.Fatalf("whitespace changed canonical bytes: %q vs %q", compact, spaced)
	}
}

func TestCanonicalize_NestedKeysSorted(t *testing.T) {
	got, err := Canonicalize([]byte(`{"z":{"y":1,"x":2},"a":[{"q":1,"p":2}]}`))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if want := `{"a":[{"p":2,"q":1}],"z":{"x":2,"y":1}}`; string(got) != want {
		t.Fatalf("nested canonical form = %q, want %q", got, want)
	}
}

func TestCanonicalize_Deterministic(t *testing.T) {
	in := []byte(`{"actor":"u1","op":"ratification","n":3}`)
	first, err := Canonicalize(in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	for i := 0; i < 100; i++ {
		next, err := Canonicalize(in)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if !bytes.Equal(first, next) {
			t.Fatalf("non-deterministic output at %d: %q vs %q", i, first, next)
		}
	}
}

func TestCanonicalize_ChangedPayloadChangesOutput(t *testing.T) {
	base, err := Canonicalize([]byte(`{"lifecycle":"ratified","v":7}`))
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	changed, err := Canonicalize([]byte(`{"lifecycle":"ratified","v":8}`))
	if err != nil {
		t.Fatalf("changed: %v", err)
	}
	if bytes.Equal(base, changed) {
		t.Fatalf("distinct payloads produced identical canonical bytes: %q", base)
	}
}

func TestCanonicalize_PreservesNumberLiterals(t *testing.T) {
	got, err := Canonicalize([]byte(`{"big":100000000000000000000,"exact":1.0,"small":0.30000000000000004}`))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	want := `{"big":100000000000000000000,"exact":1.0,"small":0.30000000000000004}`
	if string(got) != want {
		t.Fatalf("number literals not preserved: got %q, want %q", got, want)
	}
}

func TestCanonicalize_NoHTMLEscaping(t *testing.T) {
	got, err := Canonicalize([]byte(`{"note":"a<b>c&d"}`))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if want := `{"note":"a<b>c&d"}`; string(got) != want {
		t.Fatalf("HTML was escaped: got %q, want %q", got, want)
	}
}

func TestCanonicalize_TopLevelScalarsAndArrays(t *testing.T) {
	cases := map[string]string{
		`"hi"`:       `"hi"`,
		`42`:         `42`,
		`true`:       `true`,
		`null`:       `null`,
		`[3,2,1]`:    `[3,2,1]`,
		`[{"b":1}]`:  `[{"b":1}]`,
		"  \t 5 \n ": `5`,
	}
	for in, want := range cases {
		got, err := Canonicalize([]byte(in))
		if err != nil {
			t.Fatalf("Canonicalize(%q): %v", in, err)
		}
		if string(got) != want {
			t.Fatalf("Canonicalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalize_Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrEmptyCanonicalInput},
		{"whitespace only", "   \n\t ", ErrEmptyCanonicalInput},
		{"trailing object", `{}{}`, ErrTrailingCanonicalInput},
		{"trailing scalar", `1 2`, ErrTrailingCanonicalInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Canonicalize([]byte(c.in)); !errors.Is(err, c.want) {
				t.Fatalf("Canonicalize(%q) error = %v, want %v", c.in, err, c.want)
			}
		})
	}
}

func TestCanonicalize_RejectsMalformedJSON(t *testing.T) {
	for _, in := range []string{`{`, `{"a":}`, `{"a" 1}`, `nope`, `[1,2`} {
		if _, err := Canonicalize([]byte(in)); err == nil {
			t.Fatalf("Canonicalize(%q) = nil error, want a decode error", in)
		}
	}
}

func TestCanonicalize_DuplicateKeysDeterministic(t *testing.T) {
	// Encoding/json keeps the last value for a duplicated key; assert the result
	// is well-defined and stable rather than prescribing which value wins.
	first, err := Canonicalize([]byte(`{"a":1,"a":2}`))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := Canonicalize([]byte(`{"a":1,"a":2}`))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("duplicate-key canonicalization not deterministic: %q vs %q", first, second)
	}
}
