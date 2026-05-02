// Package config loads all application configuration from environment variables.
// 12-Factor App principle III: store config in the environment.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all runtime configuration for the platform.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string

	APIPort             string
	APIReadTimeoutS     int
	APIWriteTimeoutS    int
	APIIdleTimeoutS     int
	APIShutdownTimeoutS int

	RedirectPort             string
	RedirectReadTimeoutS     int
	RedirectWriteTimeoutS    int
	RedirectIdleTimeoutS     int
	RedirectShutdownTimeoutS int

	// MetricsPort is the port for the dedicated Prometheus /metrics HTTP server.
	// This server is separate from the application HTTP server so metrics are
	// never exposed through the public API gateway (WSO2 / Ingress).
	// Default: 9090 (conventional Prometheus exporter port)
	MetricsPort string

	DBPrimaryDSN       string
	DBReplicaDSN       string
	DBMaxOpenConns     int32
	DBMinOpenConns     int32
	DBConnMaxLifetimeM int
	DBConnMaxIdleTimeM int

	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	RedisPoolSize      int
	RedisMinIdleConns  int
	RedisDialTimeoutS  int
	RedisReadTimeoutS  int
	RedisWriteTimeoutS int

	RedirectCacheTTLS int
	CacheNegativeTTLS int

	OTelEnabled    bool
	OTelExporter   string
	OTelEndpoint   string
	OTelSampleRate float64

	LogLevel  string
	LogFormat string

	ShortCodeLength int
	BaseURL         string
	APIBaseURL      string

	JWTIssuer                  string
	JWTAudience                string
	JWTPublicKeyPath           string
	JWTAllowedIssuers          string
	JWTAdditionalPublicKeyPath string

	ExportStorageDir    string
	ExportSignSecret    string
	ExportDownloadTTLH  int
	ExportWorkerPollS   int
	ExportMaxWindowDays int

	WebhookWorkerEnabled       bool
	WebhookWorkerBatchSize     int
	WebhookWorkerPollIntervalS int
	WebhookWorkerHTTPTimeoutS  int
}

// Load reads all configuration from environment variables and validates them.
func Load() (*Config, error) {
	if err := LoadDotEnv(); err != nil {
		return nil, err
	}

	cfg := &Config{
		ServiceName:    getEnv("SERVICE_NAME", "url-shortener-api"),
		ServiceVersion: getEnv("SERVICE_VERSION", "dev"),
		Environment:    getEnv("ENVIRONMENT", "development"),

		APIPort:             getEnv("API_PORT", "8080"),
		APIReadTimeoutS:     getEnvInt("API_READ_TIMEOUT_S", 10),
		APIWriteTimeoutS:    getEnvInt("API_WRITE_TIMEOUT_S", 30),
		APIIdleTimeoutS:     getEnvInt("API_IDLE_TIMEOUT_S", 60),
		APIShutdownTimeoutS: getEnvInt("API_SHUTDOWN_TIMEOUT_S", 30),

		RedirectPort:             getEnv("REDIRECT_PORT", "8081"),
		RedirectReadTimeoutS:     getEnvInt("REDIRECT_READ_TIMEOUT_S", 5),
		RedirectWriteTimeoutS:    getEnvInt("REDIRECT_WRITE_TIMEOUT_S", 10),
		RedirectIdleTimeoutS:     getEnvInt("REDIRECT_IDLE_TIMEOUT_S", 60),
		RedirectShutdownTimeoutS: getEnvInt("REDIRECT_SHUTDOWN_TIMEOUT_S", 30),

		MetricsPort: getEnv("METRICS_PORT", "9090"),

		DBPrimaryDSN:       getEnv("DB_PRIMARY_DSN", ""),
		DBReplicaDSN:       getEnv("DB_REPLICA_DSN", ""),
		DBMaxOpenConns:     int32(getEnvInt("DB_MAX_OPEN_CONNS", 25)),
		DBMinOpenConns:     int32(getEnvInt("DB_MIN_OPEN_CONNS", 5)),
		DBConnMaxLifetimeM: getEnvInt("DB_CONN_MAX_LIFETIME_M", 15),
		DBConnMaxIdleTimeM: getEnvInt("DB_CONN_MAX_IDLE_TIME_M", 5),

		RedisAddr:          getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:      getEnv("REDIS_PASSWORD", ""),
		RedisDB:            getEnvInt("REDIS_DB", 0),
		RedisPoolSize:      getEnvInt("REDIS_POOL_SIZE", 10),
		RedisMinIdleConns:  getEnvInt("REDIS_MIN_IDLE_CONNS", 2),
		RedisDialTimeoutS:  getEnvInt("REDIS_DIAL_TIMEOUT_S", 5),
		RedisReadTimeoutS:  getEnvInt("REDIS_READ_TIMEOUT_S", 3),
		RedisWriteTimeoutS: getEnvInt("REDIS_WRITE_TIMEOUT_S", 3),

		RedirectCacheTTLS: getEnvInt("REDIRECT_CACHE_TTL_S", 3600),
		CacheNegativeTTLS: getEnvInt("CACHE_NEGATIVE_TTL_S", 60),

		OTelEnabled:    getEnvBool("OTEL_ENABLED", true),
		OTelExporter:   getEnv("OTEL_EXPORTER", "stdout"),
		OTelEndpoint:   getEnv("OTEL_ENDPOINT", "localhost:4317"),
		OTelSampleRate: getEnvFloat("OTEL_SAMPLE_RATE", 1.0),

		LogLevel:  getEnv("LOG_LEVEL", "info"),
		LogFormat: getEnv("LOG_FORMAT", "json"),

		ShortCodeLength: getEnvInt("SHORT_CODE_LENGTH", 7),
		BaseURL:         getEnv("BASE_URL", "http://localhost:8081"),
		APIBaseURL:      getEnv("API_BASE_URL", "http://localhost:8080"),

		JWTIssuer:                  getEnv("JWT_ISSUER", ""),
		JWTAudience:                getEnv("JWT_AUDIENCE", ""),
		JWTPublicKeyPath:           getEnv("JWT_PUBLIC_KEY_PATH", ""),
		JWTAllowedIssuers:          getEnv("JWT_ALLOWED_ISSUERS", ""),
		JWTAdditionalPublicKeyPath: getEnv("JWT_ADDITIONAL_PUBLIC_KEY_PATH", ""),

		ExportStorageDir:    getEnv("EXPORT_STORAGE_DIR", "./exports"),
		ExportSignSecret:    getEnv("EXPORT_SIGN_SECRET", ""),
		ExportDownloadTTLH:  getEnvInt("EXPORT_DOWNLOAD_TTL_H", 24),
		ExportWorkerPollS:   getEnvInt("EXPORT_WORKER_POLL_S", 5),
		ExportMaxWindowDays: getEnvInt("EXPORT_MAX_WINDOW_DAYS", 365),

		WebhookWorkerEnabled:       getEnvBool("WEBHOOK_WORKER_ENABLED", true),
		WebhookWorkerBatchSize:     getEnvInt("WEBHOOK_WORKER_BATCH_SIZE", 50),
		WebhookWorkerPollIntervalS: getEnvInt("WEBHOOK_WORKER_POLL_INTERVAL_S", 5),
		WebhookWorkerHTTPTimeoutS:  getEnvInt("WEBHOOK_WORKER_HTTP_TIMEOUT_S", 30),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadDotEnv loads environment variables from the nearest .env file found in
// the current directory or its parents. Existing environment variables win.
func LoadDotEnv() error {
	if os.Getenv("CONFIG_DISABLE_DOTENV") == "1" {
		return nil
	}

	path, err := findDotEnv()
	if err != nil || path == "" {
		return nil
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open .env: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("parse .env %s:%d: expected KEY=VALUE", path, lineNo)
		}

		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("parse .env %s:%d: empty key", path, lineNo)
		}

		if os.Getenv(key) != "" {
			continue
		}

		value = strings.TrimSpace(value)
		value = stripInlineComment(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set env %s from .env: %w", key, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read .env: %w", err)
	}

	return nil
}

func (c *Config) validate() error {
	var errs []string

	if c.Environment == "production" {
		if c.DBPrimaryDSN == "" {
			errs = append(errs, "DB_PRIMARY_DSN is required in production")
		}
		if c.DBReplicaDSN == "" {
			errs = append(errs, "DB_REPLICA_DSN is required in production")
		}
		if c.JWTIssuer == "" {
			errs = append(errs, "JWT_ISSUER is required in production")
		}
		if c.JWTPublicKeyPath == "" {
			errs = append(errs, "JWT_PUBLIC_KEY_PATH is required in production")
		}
	}

	if c.ShortCodeLength < 4 || c.ShortCodeLength > 32 {
		errs = append(errs, "SHORT_CODE_LENGTH must be between 4 and 32")
	}
	if c.OTelSampleRate < 0.0 || c.OTelSampleRate > 1.0 {
		errs = append(errs, "OTEL_SAMPLE_RATE must be between 0.0 and 1.0")
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[c.LogLevel] {
		errs = append(errs, fmt.Sprintf("LOG_LEVEL must be one of: debug, info, warn, error (got: %q)", c.LogLevel))
	}

	validExporters := map[string]bool{"stdout": true, "otlp": true}
	if c.OTelEnabled && !validExporters[c.OTelExporter] {
		errs = append(errs, fmt.Sprintf("OTEL_EXPORTER must be one of: stdout, otlp (got: %q)", c.OTelExporter))
	}
	if c.ExportDownloadTTLH <= 0 {
		errs = append(errs, "EXPORT_DOWNLOAD_TTL_H must be greater than 0")
	}
	if c.ExportWorkerPollS <= 0 {
		errs = append(errs, "EXPORT_WORKER_POLL_S must be greater than 0")
	}
	if c.WebhookWorkerBatchSize <= 0 {
		errs = append(errs, "WEBHOOK_WORKER_BATCH_SIZE must be greater than 0")
	}
	if c.WebhookWorkerPollIntervalS <= 0 {
		errs = append(errs, "WEBHOOK_WORKER_POLL_INTERVAL_S must be greater than 0")
	}
	if c.WebhookWorkerHTTPTimeoutS <= 0 {
		errs = append(errs, "WEBHOOK_WORKER_HTTP_TIMEOUT_S must be greater than 0")
	}

	if len(errs) > 0 {
		return errors.New("configuration errors:\n  - " + strings.Join(errs, "\n  - "))
	}
	return nil
}

func (c *Config) IsProduction() bool  { return c.Environment == "production" }
func (c *Config) IsDevelopment() bool { return c.Environment == "development" }

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return i
}

func getEnvBool(key string, defaultVal bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultVal
	}
	return b
}

func getEnvFloat(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

func findDotEnv() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	for {
		candidate := filepath.Join(dir, ".env")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat .env: %w", err)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func stripInlineComment(value string) string {
	inSingle := false
	inDouble := false

	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if inSingle || inDouble {
				continue
			}
			if i == 0 || value[i-1] == ' ' || value[i-1] == '\t' {
				return strings.TrimSpace(value[:i])
			}
		}
	}

	return value
}
