package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"lindesk/internal/config"
)

func TestOpenReturnsDisabledWhenDSNMissing(t *testing.T) {
	_, err := Open(context.Background(), config.DatabaseConfig{Driver: "postgres"})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Open() error = %v, want %v", err, ErrDisabled)
	}
}

func TestOpenReturnsDisabledForMemoryDriver(t *testing.T) {
	_, err := Open(context.Background(), config.DatabaseConfig{Driver: "memory", DSN: "ignored"})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Open() error = %v, want %v", err, ErrDisabled)
	}
}

func TestOpenRejectsUnsupportedDriver(t *testing.T) {
	_, err := Open(context.Background(), config.DatabaseConfig{Driver: "mysql", DSN: "mysql://example"})
	if err == nil || errors.Is(err, ErrDisabled) {
		t.Fatalf("Open() error = %v, want unsupported driver error", err)
	}
}

func TestFirstOptionAppliesDefaults(t *testing.T) {
	option := firstOption([]Options{{MaxOpenConns: 2}})
	if option.MaxOpenConns != 2 {
		t.Fatalf("MaxOpenConns = %d, want 2", option.MaxOpenConns)
	}
	if option.MaxIdleConns != DefaultOptions().MaxIdleConns {
		t.Fatalf("MaxIdleConns = %d, want default", option.MaxIdleConns)
	}
	if option.ConnMaxLifetime != 30*time.Minute {
		t.Fatalf("ConnMaxLifetime = %s, want 30m", option.ConnMaxLifetime)
	}
}
