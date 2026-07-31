package client

import (
	"context"
	"os"
	"time"
)

func tryLockContext(ctx context.Context, file *os.File, retryDelay time.Duration) (bool, error) {
	for {
		locked, err := tryLockFile(file)
		if locked || err != nil {
			return locked, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(retryDelay):
		}
	}
}
