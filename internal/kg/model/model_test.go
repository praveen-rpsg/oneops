package model

import "testing"

// The vocabulary is closed. A value outside it is a defect in whatever produced
// it, and Valid is how the graph package finds out.
func TestOriginValidity(t *testing.T) {
	for _, o := range []Origin{OriginDerived, OriginDeclared, OriginImported, OriginInferred, OriginRatified} {
		if !o.Valid() {
			t.Errorf("Origin(%q) is declared by §II but reports invalid", o)
		}
	}
	// Case matters, near-misses do not count, and the zero value is not a
	// silent default: an unset origin means nobody said where the fact came
	// from, which is exactly what must be caught.
	for _, o := range []Origin{"", "Derived", "DERIVED", "derived ", "guessed", "certain"} {
		if o.Valid() {
			t.Errorf("Origin(%q) is not declared by §II but reports valid", o)
		}
	}
}

func TestConfidenceValidity(t *testing.T) {
	for _, c := range []Confidence{ConfidenceCertain, ConfidenceHigh, ConfidenceMedium,
		ConfidenceLow, ConfidenceUnknown} {
		if !c.Valid() {
			t.Errorf("Confidence(%q) is declared by §II but reports invalid", c)
		}
	}
	for _, c := range []Confidence{"", "Certain", "CERTAIN", "sure", "derived"} {
		if c.Valid() {
			t.Errorf("Confidence(%q) is not declared by §II but reports valid", c)
		}
	}
}

// Origin and Confidence are independent fields (§V). The vocabularies must not
// overlap, or a value swapped between the two fields would validate.
func TestVocabulariesDoNotOverlap(t *testing.T) {
	origins := []Origin{OriginDerived, OriginDeclared, OriginImported, OriginInferred, OriginRatified}
	confidences := []Confidence{ConfidenceCertain, ConfidenceHigh, ConfidenceMedium,
		ConfidenceLow, ConfidenceUnknown}
	for _, o := range origins {
		for _, c := range confidences {
			if string(o) == string(c) {
				t.Errorf("%q is both an origin and a confidence; a swapped field would validate", o)
			}
		}
	}
}
