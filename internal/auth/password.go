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
	// PBKDF2 参数用于生成新密码哈希；解析时仍以哈希中的参数为准。
	passwordHashAlgorithm  = "pbkdf2-sha256"
	passwordHashIterations = 210_000
	passwordSaltLength     = 16
	passwordKeyLength      = 32
	// Demo seed 需要可重复初始化，因此使用固定的预计算哈希。
	demoPasswordHash = "pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk"
)

// NewPasswordHash 为新用户生成带随机盐的 PBKDF2-SHA256 密码哈希。
func NewPasswordHash(password string) (string, error) {
	salt := make([]byte, passwordSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}

	return passwordHash(password, salt, passwordHashIterations)
}

// VerifyPassword 同时兼容当前 PBKDF2 格式和旧 Demo SHA-256 格式，支持渐进迁移。
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

// passwordHash 将算法参数、盐和派生密钥编码成可持久化的字符串。
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
