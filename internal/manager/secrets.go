package manager

import (
	"crypto/rand"
	"errors"
	"math/big"
)

const passwordCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

var ErrPasswordLength = errors.New("password length must be > 0")

// GeneratePassword returns a cryptographically random alphanumeric string
// of the given length. Equivalent to bash's:
//
//	openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c <n>
//
// but without the SIGPIPE workaround needed in shell.
func GeneratePassword(length int) (string, error) {
	if length <= 0 {
		return "", ErrPasswordLength
	}
	max := big.NewInt(int64(len(passwordCharset)))
	out := make([]byte, length)
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = passwordCharset[idx.Int64()]
	}
	return string(out), nil
}
