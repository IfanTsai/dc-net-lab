//go:build !linux

package capture

import (
	"context"
	"errors"
	"io"
)

// Run requires AF_PACKET; the binary only ever executes inside Linux
// containers, this stub just keeps the tree compiling elsewhere.
func Run(_ context.Context, _ io.Writer, _ Options) error {
	return errors.New("capture requires linux (AF_PACKET)")
}
