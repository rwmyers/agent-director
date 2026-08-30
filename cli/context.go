package cli

import (
	"context"
	"time"
)

// contextWithTimeout is a thin alias kept in one place so the ask path reads
// cleanly and the timeout behaviour is easy to find.
func contextWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
