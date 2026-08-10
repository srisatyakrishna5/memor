package memory

import (
	"testing"
)

func TestContentID(t *testing.T) {
	id1 := ContentID("OAuth2+PKCE via Auth0")
	id2 := ContentID("OAuth2+PKCE via Auth0")
	id3 := ContentID("something else")

	if id1 != id2 {
		t.Errorf("same content should produce same ID: %s != %s", id1, id2)
	}
	if id1 == id3 {
		t.Error("different content should produce different IDs")
	}
	if len(id1) != 12 {
		t.Errorf("ID should be 12 hex chars, got %d: %s", len(id1), id1)
	}
}

func TestContentID_Normalization(t *testing.T) {
	id1 := ContentID("  Hello World  ")
	id2 := ContentID("hello world")
	if id1 != id2 {
		t.Error("ContentID should normalize whitespace and case")
	}
}

func TestParseType(t *testing.T) {
	tests := []struct {
		input string
		want  Type
	}{
		{"s", TypeSemantic},
		{"semantic", TypeSemantic},
		{"e", TypeEpisodic},
		{"episodic", TypeEpisodic},
		{"p", TypeProcedural},
		{"procedural", TypeProcedural},
		{"f", TypePreference},
		{"preference", TypePreference},
		{"invalid", ""},
	}

	for _, tt := range tests {
		got := ParseType(tt.input)
		if got != tt.want {
			t.Errorf("ParseType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTypeSortOrder(t *testing.T) {
	if TypeSemantic.SortOrder() >= TypeProcedural.SortOrder() {
		t.Error("semantic should sort before procedural")
	}
	if TypeProcedural.SortOrder() >= TypeEpisodic.SortOrder() {
		t.Error("procedural should sort before episodic")
	}
	if TypeEpisodic.SortOrder() >= TypePreference.SortOrder() {
		t.Error("episodic should sort before preference")
	}
}

func TestEntry_IsExpired(t *testing.T) {
	notExpired := Entry{Expires: 0}
	if notExpired.IsExpired() {
		t.Error("entry with Expires=0 should not be expired")
	}

	permanent := Entry{Expires: -1}
	if permanent.IsExpired() {
		t.Error("entry with Expires=-1 should be permanent")
	}

	futureExpiry := Entry{Expires: 9999999999}
	if futureExpiry.IsExpired() {
		t.Error("entry with future expiry should not be expired")
	}

	pastExpiry := Entry{Expires: 1}
	if !pastExpiry.IsExpired() {
		t.Error("entry with past expiry should be expired")
	}
}
