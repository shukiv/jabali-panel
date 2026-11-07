package userops

import (
	"errors"
	"fmt"
)

// MaxPasswordBytes is bcrypt's input limit. The panel's password hash, which
// is also the hash handed to Kratos, is bcrypt, and bcrypt refuses a longer
// password outright.
const MaxPasswordBytes = 72

// ErrPasswordTooLong means the password is longer than MaxPasswordBytes. It is
// the caller's input, not a server fault, so callers map it to a 4xx.
var ErrPasswordTooLong = errors.New("userops: password too long")

// CheckPasswordLength refuses a password bcrypt cannot hash. The limit is in
// bytes, not characters: 30 characters of a 3-byte script are 90 bytes.
// Callers that change anything before hashing (the OS account, Kratos) check
// first, so a long password fails before any of it.
func CheckPasswordLength(password string) error {
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("%w: at most %d bytes", ErrPasswordTooLong, MaxPasswordBytes)
	}
	return nil
}
