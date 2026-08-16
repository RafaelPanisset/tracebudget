package model

import (
	"testing"
	"time"
)

func TestDurationTextRoundTrip(t *testing.T) {
	original := Duration(600 * time.Millisecond)
	text, err := original.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if string(text) != "600ms" {
		t.Fatalf("got %q", text)
	}

	var decoded Duration
	if err := decoded.UnmarshalText(text); err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Fatalf("got %s, want %s", decoded, original)
	}
}
