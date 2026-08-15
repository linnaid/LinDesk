package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"lindesk/internal/config"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// 数据库未启用，不是异常失败
var ErrDisabled = errors.New("database is disabled")

type Options struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

func DefaultOptions() Options {
	return Options{
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: 30 * time.Minute,
	}
}

func Open(ctx context.Context, cfg config.DatabaseConfig, options ...Options) (*sql.DB, error) {
	driver := strings.TrimSpace(cfg.Driver)
	dsn := strings.TrimSpace(cfg.DSN)
	if driver == "" || strings.EqualFold(driver, "memory") || dsn == "" {
		return nil, ErrDisabled
	}
	if !strings.EqualFold(driver, "postgres") && !strings.EqualFold(driver, "pgx") {
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	applyOptions(db, firstOption(options))
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return db, nil
}

func IsDisabled(err error) bool {
	return errors.Is(err, ErrDisabled)
}

func firstOption(options []Options) Options {
	if len(options) == 0 {
		return DefaultOptions()
	}

	option := options[0]
	defaults := DefaultOptions()
	if option.MaxOpenConns <= 0 {
		option.MaxOpenConns = defaults.MaxOpenConns
	}
	if option.MaxIdleConns <= 0 {
		option.MaxIdleConns = defaults.MaxIdleConns
	}
	if option.ConnMaxLifetime <= 0 {
		option.ConnMaxLifetime = defaults.ConnMaxLifetime
	}

	return option
}

func applyOptions(db *sql.DB, option Options) {
	db.SetMaxOpenConns(option.MaxOpenConns)
	db.SetMaxIdleConns(option.MaxIdleConns)
	db.SetConnMaxLifetime(option.ConnMaxLifetime)
}
