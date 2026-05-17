package session

import "testing"

func TestNewIDUsesLegacyPrefix(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if _, err := ParseID(string(id)); err != nil {
		t.Fatalf("ParseID(%q) error = %v", id, err)
	}
}

func TestParseIDRejectsInvalidPrefix(t *testing.T) {
	if _, err := ParseID("not-a-session"); err == nil {
		t.Fatal("ParseID() error = nil, want error")
	}
}
