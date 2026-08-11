package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// tokenBytes is the length of a login token and of a session token.
const tokenBytes = 32

// NewToken returns a fresh random token and the SHA-256 hash of the token.
// The database stores the hash only. The raw value goes to the member.
func NewToken() (raw string, sum []byte) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand fails only when the operating system fails. The
		// program cannot make a safe token in that state, so it stops.
		panic("crypto/rand failed: " + err.Error())
	}
	digest := sha256.Sum256(buf)
	return base64.RawURLEncoding.EncodeToString(buf), digest[:]
}

// HashToken converts a raw token back into the stored hash form.
// The second result is false when the input is not a valid token.
func HashToken(raw string) (sum []byte, valid bool) {
	buf, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(buf) != tokenBytes {
		return nil, false
	}
	digest := sha256.Sum256(buf)
	return digest[:], true
}

