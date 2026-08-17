package auth

import "testing"

func TestNewPasswordHashAndVerifyPassword(t *testing.T) {
	first, err := NewPasswordHash("password123")
	if err != nil {
		t.Fatalf("NewPasswordHash() error = %v", err)
	}
	second, err := NewPasswordHash("password123")
	if err != nil {
		t.Fatalf("NewPasswordHash() second error = %v", err)
	}
	if first == second {
		t.Fatalf("password hashes are equal, want random salts")
	}
	if !VerifyPassword(first, "password123") {
		t.Fatalf("VerifyPassword() = false, want true")
	}
	if VerifyPassword(first, "wrong-password") {
		t.Fatalf("VerifyPassword() = true for wrong password")
	}
}

func TestVerifyPasswordSupportsLegacyDemoHash(t *testing.T) {
	legacyHash := "sha256:5e82c19c3ab61680865c92a9ad7e11a827e6f9b55aaec0a3e18c0418cc4745a0"
	if !VerifyPassword(legacyHash, "password123") {
		t.Fatalf("VerifyPassword() = false for legacy hash")
	}
}
