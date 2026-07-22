package postgres

import (
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	tm := time.UnixMicro(time.Now().UnixMicro()).UTC()
	id := "01HZY3ABCDEF"
	c := encodeCursor(tm, id)

	gotT, gotID, err := decodeCursor(c)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotT.Equal(tm) {
		t.Errorf("time = %v, want %v", gotT, tm)
	}
	if gotID != id {
		t.Errorf("id = %q, want %q", gotID, id)
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	if _, _, err := decodeCursor("!!!not-base64!!!"); err == nil {
		t.Error("expected base64 error")
	}
	// Valid base64 but wrong shape ("nopipe").
	if _, _, err := decodeCursor("bm9waXBl"); err == nil {
		t.Error("expected malformed-cursor error")
	}
}

func TestArgset(t *testing.T) {
	a := &argset{}
	if a.add("x") != "$1" || a.add("y") != "$2" {
		t.Fatal("argset placeholder numbering broken")
	}
	if len(a.vals) != 2 {
		t.Fatalf("vals len = %d", len(a.vals))
	}
}
