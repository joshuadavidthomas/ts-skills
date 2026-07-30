package web

import (
	"fmt"
	"math"

	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

// multipartPartOverheadBytes pays for each browser-generated multipart
// boundary and headers. Browser form parts are comfortably below 1 KiB.
const multipartPartOverheadBytes int64 = 1 << 10

// uploadBodyCap covers a maximal legal directory upload: staged file bytes,
// the manifest, namespace, and multipart framing.
func uploadBodyCap(limits tree.Limits) (int64, error) {
	if err := tree.ValidateLimits(limits); err != nil {
		return 0, err
	}
	files := int64(limits.MaxFiles)
	manifestEntryBytes, err := checkedAdd(int64(limits.MaxPathBytes), 96)
	if err != nil {
		return 0, fmt.Errorf("calculate upload manifest entry allowance: %w", err)
	}
	manifestBytes, err := checkedMultiply(files, manifestEntryBytes)
	if err != nil {
		return 0, fmt.Errorf("calculate upload manifest allowance: %w", err)
	}
	manifestBytes, err = checkedAdd(manifestBytes, 2)
	if err != nil {
		return 0, fmt.Errorf("calculate upload manifest allowance: %w", err)
	}
	parts, err := checkedAdd(files, 2)
	if err != nil {
		return 0, fmt.Errorf("calculate upload multipart parts: %w", err)
	}
	framingBytes, err := checkedMultiply(parts, multipartPartOverheadBytes)
	if err != nil {
		return 0, fmt.Errorf("calculate upload multipart framing allowance: %w", err)
	}
	cap, err := checkedAdd(limits.MaxExpandedBytes, manifestBytes)
	if err != nil {
		return 0, fmt.Errorf("calculate upload body cap: %w", err)
	}
	cap, err = checkedAdd(cap, 1024)
	if err != nil {
		return 0, fmt.Errorf("calculate upload body cap: %w", err)
	}
	cap, err = checkedAdd(cap, framingBytes)
	if err != nil {
		return 0, fmt.Errorf("calculate upload body cap: %w", err)
	}
	return cap, nil
}

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, fmt.Errorf("integer overflow")
	}
	return left + right, nil
}

func checkedMultiply(left, right int64) (int64, error) {
	if left != 0 && right > math.MaxInt64/left {
		return 0, fmt.Errorf("integer overflow")
	}
	return left * right, nil
}
