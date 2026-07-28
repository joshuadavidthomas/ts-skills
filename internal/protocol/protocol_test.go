package protocol

import (
	"math"
	"testing"

	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

func TestTreeArchiveCeiling(t *testing.T) {
	tests := map[string]struct {
		limits safetree.Limits
		want   int64
	}{
		"aggregate capacity": {
			limits: safetree.Limits{
				MaxFiles: 2, MaxPathBytes: 8, MaxDepth: 2, MaxFileBytes: 80, MaxExpandedBytes: 100,
			},
			want: 666,
		},
		"per-file capacity": {
			limits: safetree.Limits{
				MaxFiles: 2, MaxPathBytes: 8, MaxDepth: 2, MaxFileBytes: 4, MaxExpandedBytes: 100,
			},
			want: 574,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := TreeArchiveCeiling(test.limits)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("TreeArchiveCeiling() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestTreeArchiveCeilingRejectsOverflow(t *testing.T) {
	limits := safetree.Limits{
		MaxFiles:         1,
		MaxPathBytes:     1,
		MaxDepth:         1,
		MaxFileBytes:     math.MaxInt64 - 281,
		MaxExpandedBytes: math.MaxInt64 - 281,
	}
	ceiling, err := TreeArchiveCeiling(limits)
	if err != nil {
		t.Fatal(err)
	}
	if ceiling != math.MaxInt64-1 {
		t.Fatalf("TreeArchiveCeiling() = %d, want %d", ceiling, int64(math.MaxInt64-1))
	}

	limits.MaxFileBytes++
	limits.MaxExpandedBytes++
	if _, err := TreeArchiveCeiling(limits); err == nil {
		t.Fatal("TreeArchiveCeiling accepted an overflowing ceiling")
	}
}

func TestTreeArchiveCeilingGrowsWithLimits(t *testing.T) {
	small := safetree.Limits{MaxFiles: 1, MaxPathBytes: 1, MaxDepth: 1, MaxFileBytes: 1, MaxExpandedBytes: 1}
	large := safetree.Limits{MaxFiles: 2, MaxPathBytes: 2, MaxDepth: 1, MaxFileBytes: 2, MaxExpandedBytes: 4}
	smallCeiling, err := TreeArchiveCeiling(small)
	if err != nil {
		t.Fatal(err)
	}
	largeCeiling, err := TreeArchiveCeiling(large)
	if err != nil {
		t.Fatal(err)
	}
	if largeCeiling <= smallCeiling {
		t.Fatalf("larger limits ceiling = %d, want greater than %d", largeCeiling, smallCeiling)
	}
}
