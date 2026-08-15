package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultHighAmountApprovalThreshold int64 = 50_000

// 保存进程级配置。密钥应通过环境变量或密钥管理系统提供，不能提交到代码仓库。
type Config struct {
	Service  ServiceConfig  `json:"service"`
	Database DatabaseConfig `json:"database"`
	Refund   RefundConfig   `json:"refund"`
}

type ServiceConfig struct {
	Name            string   `json:"name"`
	Environment     string   `json:"environment"`
	HTTPAddr        string   `json:"http_addr"`
	ShutdownTimeout Duration `json:"shutdown_timeout"`
}

type DatabaseConfig struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

// 超过多少钱就要人工审批
type RefundConfig struct {
	HighAmountApprovalThreshold int64 `json:"high_amount_approval_threshold"`
}

// 用于从配置文件中解析人类可读的时间长度，例如 "10s"。
type Duration time.Duration

func (duration *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}

	*duration = Duration(parsed)
	return nil
}

func (duration Duration) Value() time.Duration {
	return time.Duration(duration)
}

// 先使用安全的本地默认值，再合并可选配置文件，最后应用环境变量覆盖。
// 当前项目骨架不会在加载配置时打开数据库连接。
func Load(path string) (Config, error) {
	cfg := defaultConfig()

	if path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read %q: %w", path, err)
		}

		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("decode %q: %w", path, err)
		}
	}

	if value, ok := os.LookupEnv("LINDESK_HTTP_ADDR"); ok && strings.TrimSpace(value) != "" {
		cfg.Service.HTTPAddr = value
	}
	if value, ok := os.LookupEnv("LINDESK_DATABASE_DRIVER"); ok && strings.TrimSpace(value) != "" {
		cfg.Database.Driver = value
	}
	if value, ok := os.LookupEnv("LINDESK_DATABASE_DSN"); ok && strings.TrimSpace(value) != "" {
		cfg.Database.DSN = value
	}
	if value, ok := os.LookupEnv("LINDESK_HIGH_AMOUNT_APPROVAL_THRESHOLD"); ok && strings.TrimSpace(value) != "" {
		threshold, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse LINDESK_HIGH_AMOUNT_APPROVAL_THRESHOLD: %w", err)
		}
		cfg.Refund.HighAmountApprovalThreshold = threshold
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (cfg Config) Validate() error {
	if strings.TrimSpace(cfg.Service.Name) == "" {
		return fmt.Errorf("service.name is required")
	}
	if strings.TrimSpace(cfg.Service.Environment) == "" {
		return fmt.Errorf("service.environment is required")
	}
	if strings.TrimSpace(cfg.Service.HTTPAddr) == "" {
		return fmt.Errorf("service.http_addr is required")
	}
	if cfg.Service.ShutdownTimeout <= 0 {
		return fmt.Errorf("service.shutdown_timeout must be positive")
	}
	if cfg.Refund.HighAmountApprovalThreshold <= 0 {
		return fmt.Errorf("refund.high_amount_approval_threshold must be positive")
	}

	return nil
}

func defaultConfig() Config {
	return Config{
		Service: ServiceConfig{
			Name:            "lindesk",
			Environment:     "local",
			HTTPAddr:        ":8080",
			ShutdownTimeout: Duration(10 * time.Second),
		},
		Database: DatabaseConfig{
			Driver: "postgres",
		},
		Refund: RefundConfig{
			HighAmountApprovalThreshold: defaultHighAmountApprovalThreshold,
		},
	}
}
