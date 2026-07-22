package auth

import "testing"

func TestVerifyNoMethodConfigured(t *testing.T) {
	v := NewVerifier(testIss, testAud, "", "")
	if _, err := v.Verify(mint(t, nil)); err == nil {
		t.Error("expected rejection when no signing method is configured")
	}
}
