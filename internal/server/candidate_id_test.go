package server

import "testing"

func TestCandidateIDRejectsZeroAcrossConstructors(t *testing.T) {
	if !((candidateID{}).IsZero()) {
		t.Fatal("zero value candidateID is not reported zero")
	}
	if _, err := parseCandidateID("00000000000000000000000000000000"); err == nil {
		t.Fatal("parseCandidateID accepted all-zero hexadecimal")
	}
	if _, err := candidateIDFromBytes([16]byte{}); err == nil {
		t.Fatal("candidateIDFromBytes accepted zero bytes")
	}

	id, err := newCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseCandidateID(id.String())
	if err != nil || parsed != id {
		t.Fatalf("text round-trip = %v, %v", parsed, err)
	}
	fromBytes, err := candidateIDFromBytes(id.Bytes())
	if err != nil || fromBytes != id {
		t.Fatalf("bytes round-trip = %v, %v", fromBytes, err)
	}
}
