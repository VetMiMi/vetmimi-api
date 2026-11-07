// Package config reads the environment variables the process runs with and
// checks them, reporting every problem at once.
package config

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

// Config is every environment variable the process reads; .env.example lists
// the same set.
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

	// EmailProvider is resend or smtp; SMTP suits a Gmail app password until a domain exists.
	EmailProvider string
	ResendAPIKey  string
	EmailFrom     string
	SMTPHost      string
	SMTPPort      int
	SMTPUsername  string
	SMTPPassword  string

	MediaS3Endpoint string
	MediaS3Region   string
	MediaS3Bucket   string
	// Empty on AWS, where the instance or task role grants the bucket.
	MediaS3AccessKey string
	MediaS3SecretKey string
	MediaPublicURL   string
	// Set by ECS on Fargate, which has a task role instead of an instance role.
	ContainerCredentialsURI string

	TURNHost   string
	TURNSecret string

	// Empty until the Meta app exists; Facebook and Instagram are posted by hand.
	MetaAppID        string
	MetaAppSecret    string
	MetaConfigID     string
	MetaGraphVersion string

	// Empty until the LinkedIn app exists; LinkedIn is posted by hand.
	LinkedInClientID     string
	LinkedInClientSecret string
	LinkedInAPIVersion   string

	// Empty when the portal's AI assistant is off.
	GeminiAPIKey string
	GeminiModel  string

	// Empty when the metrics listener is off.
	MetricsAddr string
}

// loader collects problems instead of stopping at the first.
type loader struct {
	getenv     func(string) string
	production bool
	problems   []string
}

const (
	envDevelopment = "development"
	envProduction  = "production"

	EmailResend = "resend"
	EmailSMTP   = "smtp"
)

var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// Load reads the configuration through getenv (os.Getenv in main, a map in
// tests). The error names variables and rules, never values: it is logged.
func Load(getenv func(string) string) (Config, error) {
	check := loader{getenv: getenv}

	env := check.withDefault("ENV", envDevelopment)
	check.production = env == envProduction

	config := Config{
		Env: env,

		DatabaseURL:     check.required("DATABASE_URL"),
		DatabaseURLTest: check.optional("DATABASE_URL_TEST"),
		RedisURL:        check.required("REDIS_URL"),
		RedisURLTest:    check.optional("REDIS_URL_TEST"),

		ServiceKey:           check.required("SERVICE_KEY"),
		PublicAPIURL:         check.required("PUBLIC_API_URL"),
		SiteURL:              check.required("SITE_URL"),
		SiteRevalidateSecret: check.requiredInProduction("SITE_REVALIDATE_SECRET"),

		EmailProvider: check.withDefault("EMAIL_PROVIDER", EmailResend),
		EmailFrom:     check.requiredInProduction("EMAIL_FROM"),

		MediaS3Endpoint:         check.requiredInProduction("MEDIA_S3_ENDPOINT"),
		MediaS3Region:           check.withDefault("MEDIA_S3_REGION", "auto"),
		MediaS3Bucket:           check.requiredInProduction("MEDIA_S3_BUCKET"),
		MediaS3AccessKey:        check.optional("MEDIA_S3_ACCESS_KEY"),
		MediaS3SecretKey:        check.optional("MEDIA_S3_SECRET_KEY"),
		ContainerCredentialsURI: check.optional("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"),
		MediaPublicURL:          check.requiredInProduction("MEDIA_PUBLIC_URL"),

		TURNHost:   check.requiredInProduction("TURN_HOST"),
		TURNSecret: check.requiredInProduction("TURN_SECRET"),

		MetaAppID:        check.optional("META_APP_ID"),
		MetaAppSecret:    check.optional("META_APP_SECRET"),
		MetaConfigID:     check.optional("META_CONFIG_ID"),
		MetaGraphVersion: check.withDefault("META_GRAPH_VERSION", "v26.0"),

		LinkedInClientID:     check.optional("LINKEDIN_CLIENT_ID"),
		LinkedInClientSecret: check.optional("LINKEDIN_CLIENT_SECRET"),
		LinkedInAPIVersion:   check.withDefault("LINKEDIN_API_VERSION", "202609"),

		GeminiAPIKey: check.optional("GEMINI_API_KEY"),
		GeminiModel:  check.withDefault("GEMINI_MODEL", "gemini-flash-latest"),

		MetricsAddr: check.optional("METRICS_ADDR"),
	}
	port := check.withDefault("PORT", "8080")
	level := check.withDefault("LOG_LEVEL", "info")
	signingSecret := check.required("SIGNING_SECRET")
	totpKey := check.required("TOTP_ENCRYPTION_KEY")

	if env != envDevelopment && env != envProduction {
		check.fail("ENV", "must be development or production")
	}
	config.Port = check.port("PORT", port)
	config.LogLevel = check.logLevel(level)
	check.absoluteURL("SITE_URL", config.SiteURL)
	check.absoluteURL("PUBLIC_API_URL", config.PublicAPIURL)
	config.SigningSecret = check.signingSecret(signingSecret)
	config.TOTPEncryptionKey = check.totpKey(totpKey)
	check.serviceKey(config.ServiceKey)
	check.emailAddress("EMAIL_FROM", config.EmailFrom)
	check.email(&config)
	check.hostPort("METRICS_ADDR", config.MetricsAddr)
	if (config.MetaAppID == "") != (config.MetaAppSecret == "") {
		check.fail("META_APP_ID", "and META_APP_SECRET must be set together")
	}
	if (config.MediaS3AccessKey == "") != (config.MediaS3SecretKey == "") {
		check.fail("MEDIA_S3_ACCESS_KEY", "and MEDIA_S3_SECRET_KEY must be set together")
	}
	if (config.LinkedInClientID == "") != (config.LinkedInClientSecret == "") {
		check.fail("LINKEDIN_CLIENT_ID", "and LINKEDIN_CLIENT_SECRET must be set together")
	}

	if len(check.problems) > 0 {
		return Config{}, errors.New("invalid configuration: " + strings.Join(check.problems, "; "))
	}
	return config, nil
}

func (config Config) Production() bool {
	return config.Env == envProduction
}

func (l *loader) fail(name, rule string) {
	l.problems = append(l.problems, name+" "+rule)
}

func (l *loader) optional(name string) string {
	return l.getenv(name)
}

func (l *loader) withDefault(name, fallback string) string {
	value := l.getenv(name)
	if value != "" {
		return value
	}
	return fallback
}

func (l *loader) required(name string) string {
	value := l.getenv(name)
	if value == "" {
		l.fail(name, "is required")
	}
	return value
}

// requiredInProduction covers mail, media and TURN: development runs without
// them, but a live site must not silently drop mail or video.
func (l *loader) requiredInProduction(name string) string {
	if l.production {
		return l.required(name)
	}
	return l.optional(name)
}

func (l *loader) email(config *Config) {
	config.ResendAPIKey = l.optional("RESEND_API_KEY")
	config.SMTPHost = l.optional("SMTP_HOST")
	smtpPort := l.withDefault("SMTP_PORT", "587")
	config.SMTPUsername = l.optional("SMTP_USERNAME")
	config.SMTPPassword = l.optional("SMTP_PASSWORD")

	switch config.EmailProvider {
	case EmailResend:
		if l.production && config.ResendAPIKey == "" {
			l.fail("RESEND_API_KEY", "is required")
		}
	case EmailSMTP:
		config.SMTPPort = l.port("SMTP_PORT", smtpPort)
		l.requiredForSMTP("SMTP_HOST", config.SMTPHost)
		l.requiredForSMTP("SMTP_USERNAME", config.SMTPUsername)
		l.requiredForSMTP("SMTP_PASSWORD", config.SMTPPassword)
	default:
		l.fail("EMAIL_PROVIDER", "must be resend or smtp")
	}
}

func (l *loader) requiredForSMTP(name, value string) {
	if value == "" {
		l.fail(name, "is required when EMAIL_PROVIDER is smtp")
	}
}

func (l *loader) port(name, value string) int {
	port, ok := parsePort(value)
	if !ok {
		l.fail(name, "must be a port number from 1 to 65535")
	}
	return port
}

func (l *loader) logLevel(value string) slog.Level {
	level, ok := logLevels[value]
	if !ok {
		l.fail("LOG_LEVEL", "must be debug, info, warn or error")
	}
	return level
}

func (l *loader) absoluteURL(name, value string) {
	if value == "" {
		return
	}
	parsed, err := url.Parse(value)
	switch {
	case err != nil || !parsed.IsAbs() || parsed.Host == "":
		l.fail(name, "must be an absolute URL")
	case l.production && parsed.Scheme != "https":
		l.fail(name, "must use https in production")
	}
}

func (l *loader) signingSecret(value string) []byte {
	if value != "" && len(value) < 32 {
		l.fail("SIGNING_SECRET", "must be at least 32 bytes")
	}
	return []byte(value)
}

func (l *loader) totpKey(value string) []byte {
	if value == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		l.fail("TOTP_ENCRYPTION_KEY", "must be standard base64 of exactly 32 bytes")
	}
	return key
}

// serviceKey is length-checked only in production, so a local key can be short.
func (l *loader) serviceKey(value string) {
	if l.production && value != "" && utf8.RuneCountInString(value) < 32 {
		l.fail("SERVICE_KEY", "must be at least 32 characters in production")
	}
}

func (l *loader) emailAddress(name, value string) {
	if value == "" {
		return
	}
	_, err := mail.ParseAddress(value)
	if err != nil {
		l.fail(name, "must be an email address such as VetMiMi <hello@example.com>")
	}
}

func (l *loader) hostPort(name, value string) {
	if value == "" {
		return
	}
	_, port, err := net.SplitHostPort(value)
	_, ok := parsePort(port)
	if err != nil || !ok {
		l.fail(name, "must be host:port")
	}
}

func parsePort(value string) (int, bool) {
	port, err := strconv.Atoi(value)
	return port, err == nil && port >= 1 && port <= 65535
}
