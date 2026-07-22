package domain

import (
	"errors"
	"testing"
)

func validObject() *ConfigObject {
	return &ConfigObject{
		Artifact:        "OneOps-Constitution-Volume-I.md",
		Version:         "1.0.0",
		Role:            RoleConstitution,
		Lifecycle:       LifecycleRatified,
		RetentionClass:  RetentionCurrentBaseline,
		Authority:       AuthorityActive,
		RetentionPolicy: "permanent",
	}
}

func TestConfigObjectValidateOK(t *testing.T) {
	if err := validObject().Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestConfigObjectValidateErrors(t *testing.T) {
	cases := map[string]func(*ConfigObject){
		"artifact":         func(c *ConfigObject) { c.Artifact = "" },
		"version":          func(c *ConfigObject) { c.Version = "v1" },
		"role":             func(c *ConfigObject) { c.Role = "nope" },
		"lifecycle":        func(c *ConfigObject) { c.Lifecycle = "nope" },
		"retention_class":  func(c *ConfigObject) { c.RetentionClass = "nope" },
		"authority":        func(c *ConfigObject) { c.Authority = "nope" },
		"retention_policy": func(c *ConfigObject) { c.RetentionPolicy = "" },
	}
	for field, mutate := range cases {
		obj := validObject()
		mutate(obj)
		err := obj.Validate()
		if err == nil {
			t.Errorf("%s: expected error, got nil", field)
			continue
		}
		ve, ok := AsValidation(err)
		if !ok {
			t.Errorf("%s: expected ValidationError, got %T", field, err)
			continue
		}
		if ve.Field != field {
			t.Errorf("expected field %q, got %q", field, ve.Field)
		}
	}
}

func TestConfigObjectCrossDimensionInvariant(t *testing.T) {
	obj := validObject()
	obj.RetentionClass = RetentionCurrentBaseline
	obj.Authority = AuthorityHistorical
	if err := obj.Validate(); err == nil {
		t.Fatal("expected cross-dimension invariant violation")
	}
}

func TestArtifactVersionValidate(t *testing.T) {
	good := &ArtifactVersion{
		CfgID:       "01ABC",
		BodyURI:     "s3://bucket/key",
		ContentHash: "0000000000000000000000000000000000000000000000000000000000000000",
		Format:      FormatMarkdown,
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
	bad := *good
	bad.ContentHash = "short"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected content_hash error")
	}
}

func TestSentinelErrors(t *testing.T) {
	if !errors.Is(ErrNotFound, ErrNotFound) {
		t.Fatal("sentinel identity broken")
	}
	ve := NewValidationError("f", "m")
	if ve.Error() == "" {
		t.Fatal("empty validation error message")
	}
}
