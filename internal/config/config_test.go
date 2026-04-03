package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/urlshortener/platform/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any inherited environment to ensure we're testing defaults.
	// In CI, test isolation prevents cross-test pollution.
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected no error with defaults, got: %v", err)
	}

	if cfg.APIPort != "8080" {
		t.Errorf("expected APIPort=8080, got %q", cfg.APIPort)
	}
	if cfg.RedirectPort != "8081" {
		t.Errorf("expected RedirectPort=8081, got %q", cfg.RedirectPort)
	}
	if cfg.ShortCodeLength != 7 {
		t.Errorf("expected ShortCodeLength=7, got %d", cfg.ShortCodeLength)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected LogLevel=info, got %q", cfg.LogLevel)
	}
	if cfg.Environment != "development" {
		t.Errorf("expected Environment=development, got %q", cfg.Environment)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	clearEnv(t)

	t.Setenv("API_PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SHORT_CODE_LENGTH", "8")
	t.Setenv("ENVIRONMENT", "staging")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.APIPort != "9090" {
		t.Errorf("expected APIPort=9090, got %q", cfg.APIPort)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected LogLevel=debug, got %q", cfg.LogLevel)
	}
	if cfg.ShortCodeLength != 8 {
		t.Errorf("expected ShortCodeLength=8, got %d", cfg.ShortCodeLength)
	}
}

func TestLoad_Validation_InvalidLogLevel(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOG_LEVEL", "verbose") // invalid

	_, err := config.Load()
	if err == nil {
		t.Error("expected validation error for invalid LOG_LEVEL, got nil")
	}
}

func TestLoad_Validation_InvalidSampleRate(t *testing.T) {
	clearEnv(t)
	t.Setenv("OTEL_SAMPLE_RATE", "1.5") // > 1.0 is invalid

	_, err := config.Load()
	if err == nil {
		t.Error("expected validation error for OTEL_SAMPLE_RATE > 1.0, got nil")
	}
}

func TestLoad_Validation_InvalidShortCodeLength(t *testing.T) {
	clearEnv(t)
	t.Setenv("SHORT_CODE_LENGTH", "2") // < 4 is invalid

	_, err := config.Load()
	if err == nil {
		t.Error("expected validation error for SHORT_CODE_LENGTH < 4, got nil")
	}
}

func TestLoad_ProductionRequiresDBDSN(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENVIRONMENT", "production")
	// DB_PRIMARY_DSN intentionally not set

	_, err := config.Load()
	if err == nil {
		t.Error("expected validation error for missing DB_PRIMARY_DSN in production, got nil")
	}
}

func TestConfig_EnvironmentHelpers(t *testing.T) {
	clearEnv(t)

	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("DB_PRIMARY_DSN", "postgres://primary")
	t.Setenv("DB_REPLICA_DSN", "postgres://replica")
	t.Setenv("JWT_ISSUER", "test-issuer")
	t.Setenv("JWT_PUBLIC_KEY_PATH", "test-public.pem")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error loading production config: %v", err)
	}
	if !cfg.IsProduction() {
		t.Error("expected IsProduction()=true")
	}
	if cfg.IsDevelopment() {
		t.Error("expected IsDevelopment()=false")
	}
}

func TestLoad_DotEnvFallbackAndEnvPrecedence(t *testing.T) {
	clearEnv(t)
	t.Setenv("CONFIG_DISABLE_DOTENV", "")

	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")
	envBody := "API_PORT=7070\nREDIS_PASSWORD=secret\nLOG_LEVEL=warn\n"
	if err := os.WriteFile(envPath, []byte(envBody), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.APIPort != "7070" {
		t.Errorf("expected APIPort from .env, got %q", cfg.APIPort)
	}
	if cfg.RedisPassword != "secret" {
		t.Errorf("expected RedisPassword from .env, got %q", cfg.RedisPassword)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected explicit env var to override .env, got %q", cfg.LogLevel)
	}
}

// clearEnv removes all known config env vars for test isolation.
// t.Setenv automatically restores them after the test.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CONFIG_DISABLE_DOTENV", "1")
	keys := []string{
		"SERVICE_NAME", "SERVICE_VERSION", "ENVIRONMENT",
		"API_PORT", "API_READ_TIMEOUT_S", "API_WRITE_TIMEOUT_S",
		"REDIRECT_PORT", "REDIRECT_READ_TIMEOUT_S",
		"DB_PRIMARY_DSN", "DB_REPLICA_DSN",
		"REDIS_ADDR", "REDIS_PASSWORD",
		"OTEL_ENABLED", "OTEL_EXPORTER", "OTEL_SAMPLE_RATE",
		"LOG_LEVEL", "LOG_FORMAT",
		"SHORT_CODE_LENGTH", "BASE_URL",
		"JWT_ISSUER", "JWT_AUDIENCE", "JWT_PUBLIC_KEY_PATH",
	}
	for _, k := range keys {
		t.Setenv(k, "") // t.Setenv restores original value after test
	}
}
