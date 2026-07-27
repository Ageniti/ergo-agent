// Package config loads and validates the ECS example configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddress       string
	InternalToken     string
	MySQLDSN          string
	AgentRoot         string
	WorkspaceRoot     string
	ShutdownTimeout   time.Duration
	WorkerID          string
	WorkerPoll        time.Duration
	WorkerLease       time.Duration
	WorkerConcurrency int
	DBMaxOpen         int
	DBMaxIdle         int
	DBConnMaxLifetime time.Duration
	OutboxWebhookURL  string
	OutboxSecret      string
	OutboxBatch       int
	OutboxTimeout     time.Duration
	ModelPricingJSON  string
}

func Load() (Config, error) {
	if err := validateEncodedEnvironment(); err != nil {
		return Config{}, err
	}
	cfg := Config{
		HTTPAddress:       getenv("AGENT_HTTP_ADDRESS", ":8080"),
		InternalToken:     os.Getenv("AGENT_INTERNAL_TOKEN"),
		MySQLDSN:          os.Getenv("AGENT_MYSQL_DSN"),
		AgentRoot:         getenv("AGENT_ROOT", "."),
		WorkspaceRoot:     getenv("AGENT_WORKSPACE_ROOT", "/workspace"),
		ShutdownTimeout:   duration("AGENT_SHUTDOWN_TIMEOUT", 30*time.Second),
		WorkerID:          getenv("AGENT_WORKER_ID", hostname()),
		WorkerPoll:        duration("AGENT_WORKER_POLL_INTERVAL", time.Second),
		WorkerLease:       duration("AGENT_WORKER_LEASE_DURATION", 60*time.Second),
		WorkerConcurrency: integer("AGENT_WORKER_CONCURRENCY", 4),
		DBMaxOpen:         integer("AGENT_DB_MAX_OPEN", 20),
		DBMaxIdle:         integer("AGENT_DB_MAX_IDLE", 10),
		DBConnMaxLifetime: duration("AGENT_DB_CONN_MAX_LIFETIME", 5*time.Minute),
		OutboxWebhookURL:  os.Getenv("AGENT_OUTBOX_WEBHOOK_URL"),
		OutboxSecret:      os.Getenv("AGENT_OUTBOX_SECRET"),
		OutboxBatch:       integer("AGENT_OUTBOX_BATCH", 50),
		OutboxTimeout:     duration("AGENT_OUTBOX_TIMEOUT", 10*time.Second),
		ModelPricingJSON:  os.Getenv("AGENT_MODEL_PRICING_JSON"),
	}
	if cfg.MySQLDSN == "" {
		return Config{}, fmt.Errorf("AGENT_MYSQL_DSN is required")
	}
	if cfg.WorkerConcurrency < 1 {
		return Config{}, fmt.Errorf("AGENT_WORKER_CONCURRENCY must be positive")
	}
	if cfg.WorkerPoll <= 0 || cfg.WorkerLease < 3*cfg.WorkerPoll {
		return Config{}, fmt.Errorf("AGENT_WORKER_LEASE_DURATION must be at least three polling intervals")
	}
	if cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("AGENT_SHUTDOWN_TIMEOUT must be positive")
	}
	if cfg.DBMaxOpen < 1 || cfg.DBMaxIdle < 0 || cfg.DBMaxIdle > cfg.DBMaxOpen {
		return Config{}, fmt.Errorf("invalid database pool limits")
	}
	if cfg.OutboxBatch < 1 || cfg.OutboxBatch > 500 || cfg.OutboxTimeout <= 0 {
		return Config{}, fmt.Errorf("invalid outbox limits")
	}
	if cfg.OutboxWebhookURL != "" {
		parsed, err := url.ParseRequestURI(cfg.OutboxWebhookURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return Config{}, fmt.Errorf("AGENT_OUTBOX_WEBHOOK_URL must be an absolute HTTP(S) URL")
		}
	}
	return cfg, nil
}

func validateEncodedEnvironment() error {
	for _, name := range []string{
		"AGENT_SHUTDOWN_TIMEOUT", "AGENT_WORKER_POLL_INTERVAL",
		"AGENT_WORKER_LEASE_DURATION", "AGENT_DB_CONN_MAX_LIFETIME",
		"AGENT_OUTBOX_TIMEOUT",
	} {
		if value := os.Getenv(name); value != "" {
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("%s must be a Go duration: %w", name, err)
			}
		}
	}
	for _, name := range []string{
		"AGENT_WORKER_CONCURRENCY", "AGENT_DB_MAX_OPEN", "AGENT_DB_MAX_IDLE",
		"AGENT_OUTBOX_BATCH",
	} {
		if value := os.Getenv(name); value != "" {
			if _, err := strconv.Atoi(value); err != nil {
				return fmt.Errorf("%s must be an integer: %w", name, err)
			}
		}
	}
	return nil
}

func (c Config) ValidateAPI() error {
	if len(c.InternalToken) < 32 {
		return fmt.Errorf("AGENT_INTERNAL_TOKEN must contain at least 32 characters")
	}
	return nil
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func duration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func integer(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown-worker"
	}
	return name
}
