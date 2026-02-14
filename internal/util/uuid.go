package util

import (
	"crypto/rand"
	"fmt"
)

// NewUUIDv4 generates a RFC4122 version 4 UUID string.
func NewUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])

	// version 4
	b[6] = (b[6] & 0x0f) | 0x40
	// variant is 10xxxxxx
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3],
		b[4], b[5],
		b[6], b[7],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15],
	)
}
