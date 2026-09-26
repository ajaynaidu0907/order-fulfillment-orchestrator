// Package mockdata generates realistic-looking but entirely synthetic data for
// seeding the order-fulfillment service and driving demos and tests. Every name,
// email, phone number, address, and product is invented for this project: there
// are no real people, companies, trademarks, or placeholder lorem-ipsum text.
//
// Generators that take a seed are deterministic: the same seed always yields the
// same records, which keeps seeding reproducible and makes the unit tests stable.
package mockdata

import (
	"math/rand"

	"github.com/google/uuid"
)

// newRand returns a deterministic PRNG seeded with seed. Every generator draws
// from its own *rand.Rand so callers can reproduce a data set exactly.
func newRand(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

// randUUID derives a RFC-4122 version-4 UUID from the given PRNG. Because the
// bytes come from the seeded source (not crypto/rand), IDs are deterministic for
// a fixed seed, which is what makes the generators reproducible.
func randUUID(r *rand.Rand) uuid.UUID {
	var b [16]byte
	// (*rand.Rand).Read never returns an error.
	_, _ = r.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return uuid.UUID(b)
}
