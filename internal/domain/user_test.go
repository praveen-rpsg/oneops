package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestUserStatus_Valid(t *testing.T) {
	for _, s := range []UserStatus{UserInvited, UserActive, UserSuspended, UserDeactivated} {
		if !s.Valid() {
			t.Errorf("%q should be a valid status", s)
		}
	}
	for _, s := range []UserStatus{"", "banned", "Active", "deleted"} {
		if s.Valid() {
			t.Errorf("%q should not be a valid status", s)
		}
	}
}

// The lifecycle is exhaustive: every ordered pair of states is asserted, so a
// new edge cannot be added without this table saying whether it is legal.
func TestUserStatus_Transitions(t *testing.T) {
	all := []UserStatus{UserInvited, UserActive, UserSuspended, UserDeactivated}
	allowed := map[UserStatus]map[UserStatus]bool{
		UserInvited:     {UserActive: true, UserDeactivated: true},
		UserActive:      {UserSuspended: true, UserDeactivated: true},
		UserSuspended:   {UserActive: true, UserDeactivated: true},
		UserDeactivated: {},
	}

	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			if got := from.CanTransitionTo(to); got != want {
				t.Errorf("%s -> %s: got %v, want %v", from, to, got, want)
			}
		}
	}
}

// Deactivation is terminal (ADR-IDENTITY-001 §8.3). Asserted on its own because
// it is the property most likely to be relaxed by someone adding a "restore"
// feature without reading the ADR.
func TestUserStatus_DeactivatedIsTerminal(t *testing.T) {
	for _, to := range []UserStatus{UserInvited, UserActive, UserSuspended, UserDeactivated} {
		if UserDeactivated.CanTransitionTo(to) {
			t.Errorf("deactivated -> %s was permitted; deactivation is terminal because the "+
				"row is retained only so audit events keep an attributable author", to)
		}
	}
}

// A transition to the same state is refused: it is a no-op that would consume a
// row version and record a change that did not happen.
func TestUserStatus_SelfTransitionIsRefused(t *testing.T) {
	for _, s := range []UserStatus{UserInvited, UserActive, UserSuspended, UserDeactivated} {
		if s.CanTransitionTo(s) {
			t.Errorf("%s -> %s was permitted; a self-transition burns a row version and "+
				"records a change that did not happen", s, s)
		}
	}
}

func TestUser_Validate(t *testing.T) {
	valid := func() *User {
		return &User{
			UserID:      "usr_1",
			Email:       "person@example.com",
			DisplayName: "Person",
			Status:      UserActive,
		}
	}

	if err := valid().Validate(); err != nil {
		t.Fatalf("a valid user was rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*User)
		field  string
	}{
		{"empty id", func(u *User) { u.UserID = "" }, "user_id"},
		{"whitespace id", func(u *User) { u.UserID = "   " }, "user_id"},
		{"empty email", func(u *User) { u.Email = "" }, "email"},
		{"whitespace email", func(u *User) { u.Email = "  " }, "email"},
		{"no at sign", func(u *User) { u.Email = "person.example.com" }, "email"},
		{"nothing before at", func(u *User) { u.Email = "@example.com" }, "email"},
		{"nothing after at", func(u *User) { u.Email = "person@" }, "email"},
		{"no dot in domain", func(u *User) { u.Email = "person@example" }, "email"},
		{"space in email", func(u *User) { u.Email = "per son@example.com" }, "email"},
		{"email too long", func(u *User) {
			u.Email = strings.Repeat("a", 250) + "@example.com"
		}, "email"},
		{"display name too long", func(u *User) {
			u.DisplayName = strings.Repeat("x", MaxDisplayNameLength+1)
		}, "display_name"},
		{"empty status", func(u *User) { u.Status = "" }, "status"},
		{"unknown status", func(u *User) { u.Status = "banned" }, "status"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := valid()
			c.mutate(u)
			err := u.Validate()
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			v, ok := AsValidation(err)
			if !ok {
				t.Fatalf("expected a ValidationError, got %T: %v", err, err)
			}
			if v.Field != c.field {
				t.Errorf("reported field %q, want %q", v.Field, c.field)
			}
		})
	}
}

// A display name at exactly the limit is accepted; the bound is inclusive.
func TestUser_DisplayNameBoundIsInclusive(t *testing.T) {
	u := &User{
		UserID:      "usr_1",
		Email:       "person@example.com",
		DisplayName: strings.Repeat("x", MaxDisplayNameLength),
		Status:      UserActive,
	}
	if err := u.Validate(); err != nil {
		t.Errorf("a display name of exactly the limit was rejected: %v", err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"  Person@Example.COM  ": "person@example.com",
		"ALLCAPS@EXAMPLE.COM":    "allcaps@example.com",
		"already@lower.com":      "already@lower.com",
		"   ":                    "",
	}
	for in, want := range cases {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewUser(t *testing.T) {
	u, err := NewUser("  Person@Example.COM ", "  Person  ")
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	if u.Email != "person@example.com" {
		t.Errorf("email = %q, want it normalised", u.Email)
	}
	if u.DisplayName != "Person" {
		t.Errorf("display name = %q, want it trimmed", u.DisplayName)
	}
	if u.Status != UserInvited {
		t.Errorf("status = %q, want invited — a new user holds no access until they accept", u.Status)
	}
	if u.UserID == "" {
		t.Error("user id was not minted")
	}

	// Two users must not share an identity. The id is server-minted precisely so
	// a caller cannot choose it (Trust Register entry 1).
	other, err := NewUser("other@example.com", "")
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	if other.UserID == u.UserID {
		t.Error("two users were minted with the same id")
	}
}

func TestNewUser_RejectsInvalidEmail(t *testing.T) {
	if _, err := NewUser("not-an-email", "Person"); err == nil {
		t.Error("NewUser accepted an address with no @")
	} else if _, ok := AsValidation(err); !ok {
		t.Errorf("expected a ValidationError, got %T", err)
	}
}

func TestUser_Active(t *testing.T) {
	for status, want := range map[UserStatus]bool{
		UserActive:      true,
		UserInvited:     false,
		UserSuspended:   false,
		UserDeactivated: false,
	} {
		u := &User{Status: status}
		if got := u.Active(); got != want {
			t.Errorf("Active() with status %q = %v, want %v", status, got, want)
		}
	}
}

func TestTransitionError(t *testing.T) {
	err := NewTransitionError(UserDeactivated, UserActive)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Error("a TransitionError should match ErrInvalidTransition so callers can " +
			"classify it without type-asserting")
	}
	if !strings.Contains(err.Error(), "deactivated") || !strings.Contains(err.Error(), "active") {
		t.Errorf("message %q should name both ends of the refused move", err.Error())
	}
}
