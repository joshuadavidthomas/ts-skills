package agentskill

import "testing"

func TestCandidateIDRejectsZeroAcrossConstructors(t *testing.T) {
	if !((CandidateID{}).IsZero()) {
		t.Fatal("zero value CandidateID is not reported zero")
	}
	if _, err := ParseCandidateID("00000000000000000000000000000000"); err == nil {
		t.Fatal("ParseCandidateID accepted all-zero hexadecimal")
	}
	if _, err := CandidateIDFromBytes([16]byte{}); err == nil {
		t.Fatal("CandidateIDFromBytes accepted zero bytes")
	}

	id, err := NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCandidateID(id.String())
	if err != nil || parsed != id {
		t.Fatalf("text round-trip = %v, %v", parsed, err)
	}
	fromBytes, err := CandidateIDFromBytes(id.Bytes())
	if err != nil || fromBytes != id {
		t.Fatalf("bytes round-trip = %v, %v", fromBytes, err)
	}
}

func TestNamespaceValidation(t *testing.T) {
	for _, namespace := range []string{"", ".", "..", " team", "team/other", "team\n"} {
		if _, err := ParseNamespace(namespace); err == nil {
			t.Errorf("ParseNamespace(%q) succeeded", namespace)
		}
	}
}
