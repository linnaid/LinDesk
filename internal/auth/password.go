package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	passwordHashAlgorithm  = "pbkdf2-sha256"
	passwordHashIterations = 210_000
	passwordSaltLength     = 16
	passwordKeyLength      = 32
	demoPasswordHash       = "pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk"
)

func NewPasswordHash(password string) (string, error) {
	salt := make([]byte, passwordSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}

	return passwordHash(password, salt, passwordHashIterations)
}

func VerifyPassword(encodedHash, password string) bool {
	parts := strings.Split(encodedHash, ":")
	if len(parts) == 4 && parts[0] == passwordHashAlgorithm {
		iterations, err := strconv.Atoi(parts[1])
		if err != nil || iterations <= 0 {
			return false
		}

		salt, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return false
		}
		expected, err := base64.RawURLEncoding.DecodeString(parts[3])
		if err != nil || len(expected) == 0 {
			return false
		}

		actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
		return err == nil && hmac.Equal(actual, expected)
	}

	if len(parts) == 2 && parts[0] == "sha256" {
		sum := sha256.Sum256([]byte("lindesk-demo:" + password))
		return hmac.Equal([]byte(parts[1]), []byte(hex.EncodeToString(sum[:])))
	}

	return false
}

func passwordHash(password string, salt []byte, iterations int) (string, error) {
	derivedKey, err := pbkdf2.Key(sha256.New, password, salt, iterations, passwordKeyLength)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"%s:%d:%s:%s",
		passwordHashAlgorithm,
		iterations,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(derivedKey),
	), nil
}
