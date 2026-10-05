// Package platform holds the process-wide plumbing every domain shares:
// configuration, logging and the database pool today, Redis and the clock as
// they arrive.
package platform

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	envDevelopment = "development"
	envProduction  = "production"
)

// Config is every setting the process reads from its environment. The list
// and the purpose of each variable are in docs/architecture.md,
// "Configuration"; .env.example mirrors it.
type Config struct {
	Port     int
	Env      string
	LogLevel slog.Level

	DatabaseURL     string
	DatabaseURLTest string
	RedisURL        string
	RedisURLTest    string

	ServiceKey           string
	SigningSecret        []byte
	TOTPEncryptionKey    []byte
	PublicAPIURL         string
	SiteURL              string
	SiteRevalidateSecret string

	ResendAPIKey string
	EmailFrom    string

	MediaS3Endpoint  string
	MediaS3Region    string
	MediaS3Bucket    string
	MediaS3AccessKey string
	MediaS3SecretKey string
	MediaPublicURL   string

	TURNHost   string
	TURNSecret string

	// MetricsAddr is empty when the metrics listener is disabled.
	MetricsAddr string
}

// Production reports whether the process runs with live data and live mail.
func (c Config) Production() bool { return c.Env == envProduction }

var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// LoadConfig reads and validates the configuration through getenv, which is
// os.Getenv in main and a map in tests. It reports every problem at once so
// a deploy is fixed in one round, and the error names variables and rules
// only, never values, because it is logged and some values are secrets.
func LoadConfig(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}

	env := l.withDefault("ENV", envDevelopment)
	l.production = env == envProduction

	c := Config{
		Env: env,

		DatabaseURL:     l.required("DATABASE_URL"),
		DatabaseURLTest: l.optional("DATABASE_URL_TEST"),
		RedisURL:        l.required("REDIS_URL"),
		RedisURLTest:    l.optional("REDIS_URL_TEST"),

		ServiceKey:           l.required("SERVICE_KEY"),
		PublicAPIURL:         l.required("PUBLIC_API_URL"),
		SiteURL:              l.required("SITE_URL"),
		SiteRevalidateSecret: l.requiredInProduction("SITE_REVALIDATE_SECRET"),

		ResendAPIKey: l.requiredInProduction("RESEND_API_KEY"),
		EmailFrom:    l.requiredInProduction("EMAIL_FROM"),

		MediaS3Endpoint:  l.requiredInProduction("MEDIA_S3_ENDPOINT"),
		MediaS3Region:    l.withDefault("MEDIA_S3_REGION", "auto"),
		MediaS3Bucket:    l.requiredInProduction("MEDIA_S3_BUCKET"),
		MediaS3AccessKey: l.requiredInProduction("MEDIA_S3_ACCESS_KEY"),
		MediaS3SecretKey: l.requiredInProduction("MEDIA_S3_SECRET_KEY"),
		MediaPublicURL:   l.requiredInProduction("MEDIA_PUBLIC_URL"),

		TURNHost:   l.requiredInProduction("TURN_HOST"),
		TURNSecret: l.requiredInProduction("TURN_SECRET"),

		MetricsAddr: l.optional("METRICS_ADDR"),
	}
	port := l.withDefault("PORT", "8080")
	level := l.withDefault("LOG_LEVEL", "info")
	signingSecret := l.required("SIGNING_SECRET")
	totpKey := l.required("TOTP_ENCRYPTION_KEY")

	if env != envDevelopment && env != envProduction {
		l.fail("ENV", "must be development or production")
	}
	c.Port = l.port("PORT", port)
	c.LogLevel = l.logLevel(level)
	l.absoluteURL("SITE_URL", c.SiteURL)
	l.absoluteURL("PUBLIC_API_URL", c.PublicAPIURL)
	c.SigningSecret = l.signingSecret(signingSecret)
	c.TOTPEncryptionKey = l.totpKey(totpKey)
	l.serviceKey(c.ServiceKey)
	l.emailAddress("EMAIL_FROM", c.EmailFrom)
	l.hostPort("METRICS_ADDR", c.MetricsAddr)

	if len(l.problems) > 0 {
		return Config{}, errors.New("invalid configuration: " + strings.Join(l.problems, "; "))
	}
	return c, nil
}

// loader collects problems instead of stopping at the first, and is the only
// place a problem is worded, so no rule can quote the value it rejected.
type loader struct {
	getenv     func(string) string
	production bool
	problems   []string
}

func (l *loader) fail(name, rule string) {
	l.problems = append(l.problems, name+" "+rule)
}

func (l *loader) optional(name string) string { return l.getenv(name) }

func (l *loader) withDefault(name, fallback string) string {
	if v := l.getenv(name); v != "" {
		return v
	}
	return fallback
}

func (l *loader) required(name string) string {
	v := l.getenv(name)
	if v == "" {
		l.fail(name, "is required")
	}
	return v
}

// requiredInProduction covers mail, media and TURN: development runs without
// them (emails are logged as "would send"), but a live site must not
// silently drop mail or video.
func (l *loader) requiredInProduction(name string) string {
	if l.production {
		return l.required(name)
	}
	return l.optional(name)
}

func (l *loader) port(name, v string) int {
	n, ok := parsePort(v)
	if !ok {
		l.fail(name, "must be a port number from 1 to 65535")
	}
	return n
}

func (l *loader) logLevel(v string) slog.Level {
	level, ok := logLevels[v]
	if !ok {
		l.fail("LOG_LEVEL", "must be debug, info, warn or error")
	}
	return level
}

func (l *loader) absoluteURL(name, v string) {
	if v == "" {
		return
	}
	u, err := url.Parse(v)
	switch {
	case err != nil || !u.IsAbs() || u.Host == "":
		l.fail(name, "must be an absolute URL")
	case l.production && u.Scheme != "https":
		l.fail(name, "must use https in production")
	}
}

func (l *loader) signingSecret(v string) []byte {
	if v != "" && len(v) < 32 {
		l.fail("SIGNING_SECRET", "must be at least 32 bytes")
	}
	return []byte(v)
}

func (l *loader) totpKey(v string) []byte {
	if v == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(key) != 32 {
		l.fail("TOTP_ENCRYPTION_KEY", "must be standard base64 of exactly 32 bytes")
	}
	return key
}

// serviceKey is only length-checked in production so a developer can use a
// memorable key locally; the live key guards every public route.
func (l *loader) serviceKey(v string) {
	if l.production && v != "" && utf8.RuneCountInString(v) < 32 {
		l.fail("SERVICE_KEY", "must be at least 32 characters in production")
	}
}

func (l *loader) emailAddress(name, v string) {
	if v == "" {
		return
	}
	if _, err := mail.ParseAddress(v); err != nil {
		l.fail(name, "must be an email address such as VetMiMi <hello@example.com>")
	}
}

func (l *loader) hostPort(name, v string) {
	if v == "" {
		return
	}
	_, port, err := net.SplitHostPort(v)
	if _, ok := parsePort(port); err != nil || !ok {
		l.fail(name, "must be host:port")
	}
}

func parsePort(v string) (int, bool) {
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 1 && n <= 65535
}
