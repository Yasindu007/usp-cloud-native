// Package config loads all application configuration from environment variables.
// This satisfies the 12-Factor App principle III (Config) — all configuration
// must come from the environment, never from files committed to source control.
//
// Design rationale:
//   - Zero external dependencies (no Viper, no godotenv in production code)
//   - Explicit over implicit: every variable is named and has a clear default
//   - Validation at startup: fail fast rather than failing mid-request
//   - Grouped by subsystem for readability and future extraction
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime configuration for the platform.
// Grouping by subsystem makes it easy to pass only the relevant
// sub-config to each infrastructure adapter.
type Config struct {
	// Service identity — used in logs, traces, and metrics labels
	ServiceName    string
	ServiceVersion string
	Environment    string // development | staging | production

	// API service HTTP configuration
	APIPort              string
	APIReadTimeoutS      int
	APIWriteTimeoutS     int
	APIIdleTimeoutS      int
	APIShutdownTimeoutS  int

	// Redirect service HTTP configuration
	RedirectPort             string
	RedirectReadTimeoutS     int
	RedirectWriteTimeoutS    int
	RedirectIdleTimeoutS     int
	RedirectShutdownTimeoutS int

	// PostgreSQL — separate DSNs enforce read/write split at the app layer.
	// In Phase 1 both point to the same instance; in Phase 4 the replica
	// DSN points to the read replica StatefulSet pod.
	DBPrimaryDSN       string
	DBReplicaDSN       string
	DBMaxOpenConns     int32
	DBMinOpenConns     int32
	DBConnMaxLifetimeM int
	DBConnMaxIdleTimeM int

	// Redis
	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	RedisPoolSize      int
	RedisMinIdleConns  int
	RedisDialTimeoutS  int
	RedisReadTimeoutS  int
	RedisWriteTimeoutS int

	// Redis TTLs
	RedirectCacheTTLS  int // TTL for cached redirect entries
	CacheNegativeTTLS  int // TTL for "not found" cache entries (negative caching)

	// OpenTelemetry
	OTelEnabled    bool
	OTelExporter   string  // stdout | otlp
	OTelEndpoint   string  // OTLP gRPC endpoint (host:port)
	OTelSampleRate float64 // 0.0–1.0

	// Logging
	LogLevel  string // debug | info | warn | error
	LogFormat string // json | text

	// Short code generation
	ShortCodeLength int
	BaseURL         string // Public-facing base URL for shortened links

	// JWT authentication (Phase 1: local mock issuer)
	JWTIssuer        string
	JWTAudience      string
	JWTPublicKeyPath string
}

// Load reads all configuration from environment variables and validates
// that required variables are set. Returns an error if validation fails
// so the caller can exit immediately (fail-fast principle).
func Load() (*Config, error) {
	cfg := &Config{
		// Service identity
		ServiceName:    getEnv("SERVICE_NAME", "url-shortener-api"),
		ServiceVersion: getEnv("SERVICE_VERSION", "dev"),
		Environment:    getEnv("ENVIRONMENT", "development"),

		// API service
		APIPort:             getEnv("API_PORT", "8080"),
		APIReadTimeoutS:     getEnvInt("API_READ_TIMEOUT_S", 10),
		APIWriteTimeoutS:    getEnvInt("API_WRITE_TIMEOUT_S", 30),
		APIIdleTimeoutS:     getEnvInt("API_IDLE_TIMEOUT_S", 60),
		APIShutdownTimeoutS: getEnvInt("API_SHUTDOWN_TIMEOUT_S", 30),

		// Redirect service
		RedirectPort:             getEnv("REDIRECT_PORT", "8081"),
		RedirectReadTimeoutS:     getEnvInt("REDIRECT_READ_TIMEOUT_S", 5),
		RedirectWriteTimeoutS:    getEnvInt("REDIRECT_WRITE_TIMEOUT_S", 10),
		RedirectIdleTimeoutS:     getEnvInt("REDIRECT_IDLE_TIMEOUT_S", 60),
		RedirectShutdownTimeoutS: getEnvInt("REDIRECT_SHUTDOWN_TIMEOUT_S", 30),

		// PostgreSQL
		DBPrimaryDSN:       getEnv("DB_PRIMARY_DSN", ""),
		DBReplicaDSN:       getEnv("DB_REPLICA_DSN", ""),
		DBMaxOpenConns:     int32(getEnvInt("DB_MAX_OPEN_CONNS", 25)),
		DBMinOpenConns:     int32(getEnvInt("DB_MIN_OPEN_CONNS", 5)),
		DBConnMaxLifetimeM: getEnvInt("DB_CONN_MAX_LIFETIME_M", 15),
		DBConnMaxIdleTimeM: getEnvInt("DB_CONN_MAX_IDLE_TIME_M", 5),

		// Redis
		RedisAddr:          getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:      getEnv("REDIS_PASSWORD", ""),
		RedisDB:            getEnvInt("REDIS_DB", 0),
		RedisPoolSize:      getEnvInt("REDIS_POOL_SIZE", 10),
		RedisMinIdleConns:  getEnvInt("REDIS_MIN_IDLE_CONNS", 2),
		RedisDialTimeoutS:  getEnvInt("REDIS_DIAL_TIMEOUT_S", 5),
		RedisReadTimeoutS:  getEnvInt("REDIS_READ_TIMEOUT_S", 3),
		RedisWriteTimeoutS: getEnvInt("REDIS_WRITE_TIMEOUT_S", 3),

		// Redis TTLs
		RedirectCacheTTLS: getEnvInt("REDIRECT_CACHE_TTL_S", 3600),
		CacheNegativeTTLS: getEnvInt("CACHE_NEGATIVE_TTL_S", 60),

		// OpenTelemetry
		OTelEnabled:    getEnvBool("OTEL_ENABLED", true),
		OTelExporter:   getEnv("OTEL_EXPORTER", "stdout"),
		OTelEndpoint:   getEnv("OTEL_ENDPOINT", "localhost:4317"),
		OTelSampleRate: getEnvFloat("OTEL_SAMPLE_RATE", 1.0),

		// Logging
		LogLevel:  getEnv("LOG_LEVEL", "info"),
		LogFormat: getEnv("LOG_FORMAT", "json"),

		// Short codes
		ShortCodeLength: getEnvInt("SHORT_CODE_LENGTH", 7),
		BaseURL:         getEnv("BASE_URL", "http://localhost:8081"),

		// JWT
		JWTIssuer:        getEnv("JWT_ISSUER", ""),
		JWTAudience:      getEnv("JWT_AUDIENCE", ""),
		JWTPublicKeyPath: getEnv("JWT_PUBLIC_KEY_PATH", ""),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate performs semantic validation on the loaded configuration.
// This catches misconfiguration at startup rather than at request time.
func (c *Config) validate() error {
	var errs []string

	// In production, DSNs are required. In development we allow empty
	// (services start without DB for structural testing).
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

	if len(errs) > 0 {
		return errors.New("configuration errors:\n  - " + strings.Join(errs, "\n  - "))
	}

	return nil
}

// IsProduction returns true when running in the production environment.
// Used by components that need environment-specific behavior (e.g., log level,
// OTel sample rate, TLS enforcement).
func (c *Config) IsProduction() bool {
	return c.Environment == "production"
}

// IsDevelopment returns true when running in the development environment.
func (c *Config) IsDevelopment() bool {
	return c.Environment == "development"
}

// ---- Environment variable helpers ----
// These are package-private helpers. Using them internally keeps the Load()
// function readable without adding an external dependency like godotenv.

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