package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileLockSerializesOpenDescriptors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "write.lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	})
	second, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	})

	if locked, err := tryLockFile(first); err != nil || !locked {
		t.Fatalf("first lock = %v, %v", locked, err)
	}
	if locked, err := tryLockFile(second); err != nil || locked {
		t.Fatalf("contending lock = %v, %v", locked, err)
	}
	if err := unlockFile(first); err != nil {
		t.Fatal(err)
	}
	if locked, err := tryLockFile(second); err != nil || !locked {
		t.Fatalf("lock after release = %v, %v", locked, err)
	}
	if err := unlockFile(second); err != nil {
		t.Fatal(err)
	}
}
