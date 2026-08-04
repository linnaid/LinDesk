package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesDefaults(t *testing.T) {
	t.Setenv("LINDESK_HTTP_ADDR", "")
	t.Setenv("LINDESK_DATABASE_DSN", "")
	t.Setenv("LINDESK_HIGH_AMOUNT_APPROVAL_THRESHOLD", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Service.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want :8080", cfg.Service.HTTPAddr)
	}
	if cfg.Refund.HighAmountApprovalThreshold != 50_000 {
		t.Fatalf("HighAmountApprovalThreshold = %d, want 50000", cfg.Refund.HighAmountApprovalThreshold)
	}
}

func TestLoadAppliesFileAndEnvironmentOverrides(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	contents := []byte(`{
  "service": {
    "name": "lindesk-test",
    "environment": "test",
    "http_addr": ":8081",
    "shutdown_timeout": "2s"
  },
  "database": {
    "driver": "postgres",
    "dsn": "postgres://from-file"
  },
  "refund": {
    "high_amount_approval_threshold": 60000
  }
}`)
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("LINDESK_HTTP_ADDR", ":9090")
	t.Setenv("LINDESK_DATABASE_DSN", "postgres://from-env")
	t.Setenv("LINDESK_HIGH_AMOUNT_APPROVAL_THRESHOLD", "70000")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Service.HTTPAddr != ":9090" {
		t.Fatalf("HTTPAddr = %q, want :9090", cfg.Service.HTTPAddr)
	}
	if cfg.Database.DSN != "postgres://from-env" {
		t.Fatalf("DSN = %q, want environment value", cfg.Database.DSN)
	}
	if cfg.Refund.HighAmountApprovalThreshold != 70_000 {
		t.Fatalf("HighAmountApprovalThreshold = %d, want 70000", cfg.Refund.HighAmountApprovalThreshold)
	}
	if cfg.Service.ShutdownTimeout.Value() != 2*time.Second {
		t.Fatalf("ShutdownTimeout = %s, want 2s", cfg.Service.ShutdownTimeout.Value())
	}
}

func TestLoadReadsLocalExampleConfiguration(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "local.example.json")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Service.ShutdownTimeout.Value() != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %s, want 10s", cfg.Service.ShutdownTimeout.Value())
	}
}
