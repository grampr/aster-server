package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const tokenEntropyBytes = 32

type TokenIssuer struct{}

func (TokenIssuer) NewAccessToken() (string, []byte, error) {
	return newOpaqueToken("aster_at_")
}

func (TokenIssuer) NewRefreshToken() (string, []byte, error) {
	return newOpaqueToken("aster_rt_")
}

func HashToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func newOpaqueToken(prefix string) (string, []byte, error) {
	random := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(random); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}
	token := prefix + base64.RawURLEncoding.EncodeToString(random)
	return token, HashToken(token), nil
}
