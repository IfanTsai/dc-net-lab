package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewID returns a sortable unique ID such as "lab-018f3c2a9e4d-a1b2c3".
// The millisecond timestamp prefix keeps IDs roughly time-ordered.
func NewID(prefix string) string {
	var b [3]byte
	_, _ = rand.Read(b[:])

	return fmt.Sprintf("%s-%012x-%s", prefix, time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}
