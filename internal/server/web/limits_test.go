package web

import (
	"math"
	"strings"
	"testing"

	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

func TestUploadBodyCapCoversPrototypeLimits(t *testing.T) {
	limits := tree.PrototypeLimits()
	cap, err := uploadBodyCap(limits)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes := int64(limits.MaxFiles)*(int64(limits.MaxPathBytes)+96) + 2
	framingBytes := int64(limits.MaxFiles+2) * multipartPartOverheadBytes
	want := limits.MaxExpandedBytes + manifestBytes + 1024 + framingBytes
	if cap != want {
		t.Fatalf("upload body cap = %d, want %d", cap, want)
	}
}

func TestUploadBodyCapRejectsOverflow(t *testing.T) {
	_, err := uploadBodyCap(tree.Limits{
		MaxFiles:         math.MaxInt,
		MaxPathBytes:     math.MaxInt,
		MaxDepth:         1,
		MaxFileBytes:     math.MaxInt64,
		MaxExpandedBytes: math.MaxInt64,
	})
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflowing upload body cap error = %v, want overflow", err)
	}
}
