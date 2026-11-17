package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for new hashes (OWASP's minimum is m=19 MiB, t=2, p=1).
// They are stored in each hash, so they can be raised later without breaking old ones.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Password length limits, in characters.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 256
)

// ErrWeakPassword is returned for a password outside the length limits.
var ErrWeakPassword = fmt.Errorf("the password must have %d to %d characters", MinPasswordLen, MaxPasswordLen)

var errBadHash = errors.New("malformed password hash")

// CheckPassword enforces the length limits.
func CheckPassword(pw string) error {
	if n := utf8.RuneCountInString(pw); n < MinPasswordLen || n > MaxPasswordLen {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns an argon2id hash in PHC form:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the hash, in constant time.
func VerifyPassword(pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(pw), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is checked against when the account does not exist, so a wrong
// name costs as much time as a wrong password and reveals nothing.
var dummyHash = func() string {
	h, err := HashPassword("not a real password, only spends the same time")
	if err != nil {
		panic(err)
	}
	return h
}()
