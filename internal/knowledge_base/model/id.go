package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// NewID returns a 26-char Crockford-base32 ULID: 48-bit Unix-ms prefix keeps
// ordering lexical, 80 random bits make collisions negligible.
func NewID() string {
	return NewIDAt(time.Now())
}

var randMu sync.Mutex

func NewIDAt(t time.Time) string {
	ms := uint64(t.UnixMilli())
	randMu.Lock()
	var b [10]byte
	_, _ = rand.Read(b[:])
	randMu.Unlock()
	raw := make([]byte, 16)
	binary.BigEndian.PutUint64(raw[:8], ms)
	copy(raw[8:], b[:])
	return encodeBase32(raw)
}

const base32Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func encodeBase32(src []byte) string {
	var sb strings.Builder
	sb.Grow(26)
	var bits, buf uint
	for _, c := range src {
		buf = buf<<8 | uint(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			sb.WriteByte(base32Alphabet[(buf>>bits)&31])
		}
	}
	if bits > 0 {
		sb.WriteByte(base32Alphabet[(buf << (5 - bits) & 31)])
	}
	return sb.String()
}

// ContentHash is a stable fingerprint of normalized text for exact dedup.
func ContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ChunkID derives a deterministic, content-addressed chunk id from its
// content and position, so identical inputs replay byte-stably.
func ChunkID(order int, text string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00", order)
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}
