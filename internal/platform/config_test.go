package platform

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var testTOTPKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

// developmentEnv sets only the variables every environment requires.
func developmentEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":        "postgres://localhost:5432/vetmimi?sslmode=disable",
		"REDIS_URL":           "redis://localhost:6379/0",
		"SERVICE_KEY":         "local-key",
		"SIGNING_SECRET":      strings.Repeat("s", 32),
		"TOTP_ENCRYPTION_KEY": testTOTPKey,
		"SITE_URL":            "http://localhost:3000",
		"PUBLIC_API_URL":      "http://localhost:8080",
	}
}

func productionEnv() map[string]string {
	return map[string]string{
		"PORT":                   "9000",
		"ENV":                    "production",
		"LOG_LEVEL":              "warn",
		"DATABASE_URL":           "postgres://db.internal:5432/vetmimi",
		"DATABASE_URL_TEST":      "postgres://db.internal:5432/vetmimi_test",
		"REDIS_URL":              "redis://redis.internal:6379/0",
		"REDIS_URL_TEST":         "redis://redis.internal:6379/1",
		"SERVICE_KEY":            strings.Repeat("k", 32),
		"SIGNING_SECRET":         strings.Repeat("s", 32),
		"TOTP_ENCRYPTION_KEY":    testTOTPKey,
		"PUBLIC_API_URL":         "https://api.example.com",
		"SITE_URL":               "https://www.example.com",
		"SITE_REVALIDATE_SECRET": "revalidate",
		"RESEND_API_KEY":         "re_test",
		"EMAIL_FROM":             "VetMiMi <hello@example.com>",
		"MEDIA_S3_ENDPOINT":      "https://s3.example.com",
		"MEDIA_S3_REGION":        "ap-southeast-2",
		"MEDIA_S3_BUCKET":        "vetmimi-media",
		"MEDIA_S3_ACCESS_KEY":    "access",
		"MEDIA_S3_SECRET_KEY":    "secret",
		"MEDIA_PUBLIC_URL":       "https://media.example.com",
		"TURN_HOST":              "turn.example.com:3478",
		"TURN_SECRET":            "turn",
		"METRICS_ADDR":           "127.0.0.1:9090",
	}
}

func load(env map[string]string) (Config, error) {
	return LoadConfig(func(name string) string { return env[name] })
}

func with(env map[string]string, name, value string) map[string]string {
	env = maps.Clone(env)
	env[name] = value
	return env
}

func TestLoadConfigProduction(t *testing.T) {
	c, err := load(productionEnv())
	require.NoError(t, err)

	require.True(t, c.Production())
	require.Equal(t, 9000, c.Port)
	require.Equal(t, slog.LevelWarn, c.LogLevel)
	require.Equal(t, "https://www.example.com", c.SiteURL)
	require.Equal(t, "ap-southeast-2", c.MediaS3Region)
	require.Equal(t, "127.0.0.1:9090", c.MetricsAddr)
	require.Len(t, c.SigningSecret, 32)
	require.Equal(t, bytes.Repeat([]byte{7}, 32), c.TOTPEncryptionKey)
}

func TestLoadConfigDevelopmentDefaults(t *testing.T) {
	c, err := load(developmentEnv())
	require.NoError(t, err)

	require.False(t, c.Production())
	require.Equal(t, "development", c.Env)
	require.Equal(t, 8080, c.Port)
	require.Equal(t, slog.LevelInfo, c.LogLevel)
	require.Equal(t, "auto", c.MediaS3Region)
	require.Empty(t, c.MetricsAddr)
	require.Empty(t, c.ResendAPIKey)
}

func TestLoadConfigNamesEachMissingRequiredVariable(t *testing.T) {
	for name := range developmentEnv() {
		t.Run(name, func(t *testing.T) {
			_, err := load(with(developmentEnv(), name, ""))
			require.ErrorContains(t, err, name+" is required")
		})
	}
}

func TestLoadConfigReportsEveryProblemAtOnce(t *testing.T) {
	_, err := load(map[string]string{"LOG_LEVEL": "loud"})
	require.Error(t, err)
	for name := range developmentEnv() {
		require.ErrorContains(t, err, name+" is required")
	}
	require.ErrorContains(t, err, "LOG_LEVEL must be")
}

func TestProductionRequiresEverySecret(t *testing.T) {
	optional := []string{"DATABASE_URL_TEST", "REDIS_URL_TEST", "METRICS_ADDR"}
	defaulted := []string{"PORT", "ENV", "LOG_LEVEL", "MEDIA_S3_REGION"}
	for name := range productionEnv() {
		if slices.Contains(optional, name) || slices.Contains(defaulted, name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			_, err := load(with(productionEnv(), name, ""))
			require.ErrorContains(t, err, name+" is required")
		})
	}

	_, err := load(with(developmentEnv(), "RESEND_API_KEY", ""))
	require.NoError(t, err, "development logs emails instead of sending them")
}

func TestLoadConfigOptionalInProduction(t *testing.T) {
	env := productionEnv()
	for _, name := range []string{"DATABASE_URL_TEST", "REDIS_URL_TEST", "METRICS_ADDR"} {
		env[name] = ""
	}
	c, err := load(env)
	require.NoError(t, err)
	require.Empty(t, c.MetricsAddr)
}

func TestLoadConfigRules(t *testing.T) {
	cases := []struct {
		name, variable, value, rule string
		env                         map[string]string
	}{
		{"totp key of 31 bytes", "TOTP_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 31)), "TOTP_ENCRYPTION_KEY must be standard base64 of exactly 32 bytes", developmentEnv()},
		{"totp key not base64", "TOTP_ENCRYPTION_KEY", "not base64!", "TOTP_ENCRYPTION_KEY must be standard base64 of exactly 32 bytes", developmentEnv()},
		{"short signing secret", "SIGNING_SECRET", strings.Repeat("s", 31), "SIGNING_SECRET must be at least 32 bytes", developmentEnv()},
		{"unknown log level", "LOG_LEVEL", "loud", "LOG_LEVEL must be debug, info, warn or error", developmentEnv()},
		{"unknown env", "ENV", "staging", "ENV must be development or production", developmentEnv()},
		{"port zero", "PORT", "0", "PORT must be a port number from 1 to 65535", developmentEnv()},
		{"port too high", "PORT", "65536", "PORT must be a port number from 1 to 65535", developmentEnv()},
		{"port not a number", "PORT", "http", "PORT must be a port number from 1 to 65535", developmentEnv()},
		{"relative site url", "SITE_URL", "/home", "SITE_URL must be an absolute URL", developmentEnv()},
		{"api url without host", "PUBLIC_API_URL", "http://", "PUBLIC_API_URL must be an absolute URL", developmentEnv()},
		{"http site url in production", "SITE_URL", "http://www.example.com", "SITE_URL must use https in production", productionEnv()},
		{"http api url in production", "PUBLIC_API_URL", "http://api.example.com", "PUBLIC_API_URL must use https in production", productionEnv()},
		{"short service key in production", "SERVICE_KEY", strings.Repeat("k", 31), "SERVICE_KEY must be at least 32 characters in production", productionEnv()},
		{"malformed sender", "EMAIL_FROM", "VetMiMi hello at example", "EMAIL_FROM must be an email address", developmentEnv()},
		{"metrics address without port", "METRICS_ADDR", "127.0.0.1", "METRICS_ADDR must be host:port", developmentEnv()},
		{"metrics address with bad port", "METRICS_ADDR", "127.0.0.1:metrics", "METRICS_ADDR must be host:port", developmentEnv()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(with(tc.env, tc.variable, tc.value))
			require.ErrorContains(t, err, tc.rule)
		})
	}
}

func TestConfigErrorNeverContainsValues(t *testing.T) {
	env := with(productionEnv(), "SIGNING_SECRET", "short-secret-value")
	invalid := map[string]string{
		"SERVICE_KEY":         "short-service-key",
		"TOTP_ENCRYPTION_KEY": "c2hvcnQtdG90cC1rZXk=",
		"PORT":                "port-value",
		"LOG_LEVEL":           "level-value",
		"SITE_URL":            "site-url-value",
		"PUBLIC_API_URL":      "http://api-url-value.example",
		"EMAIL_FROM":          "email-from-value",
		"METRICS_ADDR":        "metrics-addr-value",
	}
	maps.Copy(env, invalid)

	_, err := load(env)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "short-secret-value")
	for name, value := range invalid {
		require.ErrorContains(t, err, name)
		require.NotContains(t, err.Error(), value)
	}
}

// TestEnvExampleMatchesConfig keeps .env.example the complete list a
// developer copies: a variable LoadConfig reads but the example omits would
// surface only as a start-up error, and a stale one misleads.
func TestEnvExampleMatchesConfig(t *testing.T) {
	var read []string
	_, _ = LoadConfig(func(name string) string {
		read = append(read, name)
		return ""
	})

	example := envExample(t)
	require.ElementsMatch(t, slices.Compact(slices.Sorted(slices.Values(read))), slices.Collect(maps.Keys(example)))
}

func TestEnvExampleLeavesSecretsEmpty(t *testing.T) {
	example := envExample(t)
	for _, name := range []string{
		"SERVICE_KEY", "SIGNING_SECRET", "TOTP_ENCRYPTION_KEY", "SITE_REVALIDATE_SECRET",
		"RESEND_API_KEY", "MEDIA_S3_ACCESS_KEY", "MEDIA_S3_SECRET_KEY", "TURN_SECRET",
	} {
		require.Contains(t, example, name)
		require.Empty(t, example[name], name)
	}
}

func envExample(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("../../.env.example")
	require.NoError(t, err)
	defer f.Close()

	vars := map[string]string{}
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "line without '=': %q", line)
		require.NotContains(t, vars, name, "%s is listed twice", name)
		vars[name] = value
	}
	require.NoError(t, lines.Err())
	return vars
}
