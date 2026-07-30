package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

type candidateID struct{ id [16]byte }

func newCandidateID() (candidateID, error) {
	var id candidateID
	if _, err := rand.Read(id.id[:]); err != nil {
		return candidateID{}, fmt.Errorf("generate candidate identity: %w", err)
	}
	return id, nil
}

// candidateIDFromBytes constructs a candidate identity from its persisted
// 16-byte form. It is the persistence seam for storage; other construction
// should go through newCandidateID or parseCandidateID.
func candidateIDFromBytes(raw [16]byte) (candidateID, error) {
	id := candidateID{id: raw}
	if id.IsZero() {
		return candidateID{}, fmt.Errorf("candidate identity must not be zero")
	}
	return id, nil
}

func parseCandidateID(src string) (candidateID, error) {
	var id candidateID
	if len(src) != 32 {
		return id, fmt.Errorf("candidate identity must contain 32 lowercase hexadecimal characters")
	}
	for _, ch := range src {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return id, fmt.Errorf("candidate identity must use lowercase hexadecimal characters")
		}
	}
	if _, err := hex.Decode(id.id[:], []byte(src)); err != nil {
		return candidateID{}, fmt.Errorf("parse candidate identity: %w", err)
	}
	if id.IsZero() {
		return candidateID{}, fmt.Errorf("candidate identity must not be zero")
	}
	return id, nil
}

func (id candidateID) String() string { return hex.EncodeToString(id.id[:]) }

// Bytes returns the identity's persisted 16-byte form.
func (id candidateID) Bytes() [16]byte { return id.id }

func (id candidateID) IsZero() bool { return id.id == ([16]byte{}) }
