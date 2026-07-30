package treearchive

import "testing"

func TestMaxBytesCoversPrototypeLimits(t *testing.T) {
	const payload = 128 << 20
	const entries = 2048
	const names = 1024
	want := int64(payload + entries*(256+2*names) + 22)
	if MaxBytes != want {
		t.Fatalf("MaxBytes = %d, want %d", MaxBytes, want)
	}
}
