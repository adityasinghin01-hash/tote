// Package lock seals and unseals box bytes with a key + PIN (SPEC §4).
package lock

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"

	"filippo.io/age"
)

// WorkFactor is the scrypt cost (log2 N). 18 is age's default, ~1s per try on
// a laptop. Tests lower it to stay fast.
var WorkFactor = 18

// ErrWrongCode means the key or PIN does not open this box.
var ErrWrongCode = errors.New("wrong key or PIN")

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a fresh random key (26 base32 chars, 128 bits) and a
// 6-digit PIN.
func NewSecret() (key, pin string, err error) {
	raw := make([]byte, 16)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", "", err
	}
	return strings.ToLower(b32.EncodeToString(raw)), fmt.Sprintf("%06d", n.Int64()), nil
}

// ValidKey reports whether s looks like a key made by NewSecret.
func ValidKey(s string) bool {
	b, err := b32.DecodeString(strings.ToUpper(s))
	return err == nil && len(b) == 16
}

// ValidPIN reports whether s is exactly six digits.
func ValidPIN(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func passphrase(key, pin string) (string, error) {
	if !ValidKey(key) {
		return "", fmt.Errorf("key must be 26 base32 characters")
	}
	if !ValidPIN(pin) {
		return "", fmt.Errorf("PIN must be 6 digits")
	}
	return strings.ToLower(key) + ":" + pin, nil
}

// Seal returns a writer that encrypts everything written to it into dst.
// The caller must Close it to flush the final block.
func Seal(dst io.Writer, key, pin string) (io.WriteCloser, error) {
	pass, err := passphrase(key, pin)
	if err != nil {
		return nil, err
	}
	r, err := age.NewScryptRecipient(pass)
	if err != nil {
		return nil, err
	}
	r.SetWorkFactor(WorkFactor)
	return age.Encrypt(dst, r)
}

// Unseal returns a reader of the decrypted contents of src.
func Unseal(src io.Reader, key, pin string) (io.Reader, error) {
	pass, err := passphrase(key, pin)
	if err != nil {
		return nil, err
	}
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		return nil, err
	}
	id.SetMaxWorkFactor(22)
	r, err := age.Decrypt(src, id)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrWrongCode
		}
		return nil, err
	}
	return r, nil
}
