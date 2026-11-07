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
	env := loader{getenv: getenv}

	environment := env.withDefault("ENV", envDevelopment)
	env.production = environment == envProduction

	config := Config{
		Env: environment,

		DatabaseURL:     env.required("DATABASE_URL"),
		DatabaseURLTest: env.optional("DATABASE_URL_TEST"),
		RedisURL:        env.required("REDIS_URL"),
		RedisURLTest:    env.optional("REDIS_URL_TEST"),

		ServiceKey:           env.required("SERVICE_KEY"),
		PublicAPIURL:         env.required("PUBLIC_API_URL"),
		SiteURL:              env.required("SITE_URL"),
		SiteRevalidateSecret: env.requiredInProduction("SITE_REVALIDATE_SECRET"),

		EmailProvider: env.withDefault("EMAIL_PROVIDER", EmailResend),
		EmailFrom:     env.requiredInProduction("EMAIL_FROM"),

		MediaS3Endpoint:         env.requiredInProduction("MEDIA_S3_ENDPOINT"),
		MediaS3Region:           env.withDefault("MEDIA_S3_REGION", "auto"),
		MediaS3Bucket:           env.requiredInProduction("MEDIA_S3_BUCKET"),
		MediaS3AccessKey:        env.optional("MEDIA_S3_ACCESS_KEY"),
		MediaS3SecretKey:        env.optional("MEDIA_S3_SECRET_KEY"),
		ContainerCredentialsURI: env.optional("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"),
		MediaPublicURL:          env.requiredInProduction("MEDIA_PUBLIC_URL"),

		TURNHost:   env.requiredInProduction("TURN_HOST"),
		TURNSecret: env.requiredInProduction("TURN_SECRET"),

		MetaAppID:        env.optional("META_APP_ID"),
		MetaAppSecret:    env.optional("META_APP_SECRET"),
		MetaConfigID:     env.optional("META_CONFIG_ID"),
		MetaGraphVersion: env.withDefault("META_GRAPH_VERSION", "v26.0"),

		LinkedInClientID:     env.optional("LINKEDIN_CLIENT_ID"),
		LinkedInClientSecret: env.optional("LINKEDIN_CLIENT_SECRET"),
		LinkedInAPIVersion:   env.withDefault("LINKEDIN_API_VERSION", "202609"),

		GeminiAPIKey: env.optional("GEMINI_API_KEY"),
		GeminiModel:  env.withDefault("GEMINI_MODEL", "gemini-flash-latest"),

		MetricsAddr: env.optional("METRICS_ADDR"),
	}
	port := env.withDefault("PORT", "8080")
	level := env.withDefault("LOG_LEVEL", "info")
	signingSecret := env.required("SIGNING_SECRET")
	totpKey := env.required("TOTP_ENCRYPTION_KEY")

	if environment != envDevelopment && environment != envProduction {
		env.fail("ENV", "must be development or production")
	}
	config.Port = env.port("PORT", port)
	config.LogLevel = env.logLevel(level)
	env.absoluteURL("SITE_URL", config.SiteURL)
	env.absoluteURL("PUBLIC_API_URL", config.PublicAPIURL)
	config.SigningSecret = env.signingSecret(signingSecret)
	config.TOTPEncryptionKey = env.totpKey(totpKey)
	env.serviceKey(config.ServiceKey)
	env.emailAddress("EMAIL_FROM", config.EmailFrom)
	env.email(&config)
	env.hostPort("METRICS_ADDR", config.MetricsAddr)
	if (config.MetaAppID == "") != (config.MetaAppSecret == "") {
		env.fail("META_APP_ID", "and META_APP_SECRET must be set together")
	}
	if (config.MediaS3AccessKey == "") != (config.MediaS3SecretKey == "") {
		env.fail("MEDIA_S3_ACCESS_KEY", "and MEDIA_S3_SECRET_KEY must be set together")
	}
	if (config.LinkedInClientID == "") != (config.LinkedInClientSecret == "") {
		env.fail("LINKEDIN_CLIENT_ID", "and LINKEDIN_CLIENT_SECRET must be set together")
	}

	if len(env.problems) > 0 {
		return Config{}, errors.New("invalid configuration: " + strings.Join(env.problems, "; "))
	}
	return config, nil
}

func (config Config) Production() bool {
	return config.Env == envProduction
}

func (env *loader) fail(name, rule string) {
	env.problems = append(env.problems, name+" "+rule)
}

func (env *loader) optional(name string) string {
	return env.getenv(name)
}

func (env *loader) withDefault(name, fallback string) string {
	value := env.getenv(name)
	if value != "" {
		return value
	}
	return fallback
}

func (env *loader) required(name string) string {
	value := env.getenv(name)
	if value == "" {
		env.fail(name, "is required")
	}
	return value
}

// requiredInProduction covers mail, media and TURN: development runs without
// them, but a live site must not silently drop mail or video.
func (env *loader) requiredInProduction(name string) string {
	if env.production {
		return env.required(name)
	}
	return env.optional(name)
}

func (env *loader) email(config *Config) {
	config.ResendAPIKey = env.optional("RESEND_API_KEY")
	config.SMTPHost = env.optional("SMTP_HOST")
	smtpPort := env.withDefault("SMTP_PORT", "587")
	config.SMTPUsername = env.optional("SMTP_USERNAME")
	config.SMTPPassword = env.optional("SMTP_PASSWORD")

	switch config.EmailProvider {
	case EmailResend:
		if env.production && config.ResendAPIKey == "" {
			env.fail("RESEND_API_KEY", "is required")
		}
	case EmailSMTP:
		config.SMTPPort = env.port("SMTP_PORT", smtpPort)
		env.requiredForSMTP("SMTP_HOST", config.SMTPHost)
		env.requiredForSMTP("SMTP_USERNAME", config.SMTPUsername)
		env.requiredForSMTP("SMTP_PASSWORD", config.SMTPPassword)
	default:
		env.fail("EMAIL_PROVIDER", "must be resend or smtp")
	}
}

func (env *loader) requiredForSMTP(name, value string) {
	if value == "" {
		env.fail(name, "is required when EMAIL_PROVIDER is smtp")
	}
}

func (env *loader) port(name, value string) int {
	port, ok := parsePort(value)
	if !ok {
		env.fail(name, "must be a port number from 1 to 65535")
	}
	return port
}

func (env *loader) logLevel(value string) slog.Level {
	level, ok := logLevels[value]
	if !ok {
		env.fail("LOG_LEVEL", "must be debug, info, warn or error")
	}
	return level
}

func (env *loader) absoluteURL(name, value string) {
	if value == "" {
		return
	}
	parsed, err := url.Parse(value)
	switch {
	case err != nil || !parsed.IsAbs() || parsed.Host == "":
		env.fail(name, "must be an absolute URL")
	case env.production && parsed.Scheme != "https":
		env.fail(name, "must use https in production")
	}
}

func (env *loader) signingSecret(value string) []byte {
	if value != "" && len(value) < 32 {
		env.fail("SIGNING_SECRET", "must be at least 32 bytes")
	}
	return []byte(value)
}

func (env *loader) totpKey(value string) []byte {
	if value == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		env.fail("TOTP_ENCRYPTION_KEY", "must be standard base64 of exactly 32 bytes")
	}
	return key
}

// serviceKey is length-checked only in production, so a local key can be short.
func (env *loader) serviceKey(value string) {
	if env.production && value != "" && utf8.RuneCountInString(value) < 32 {
		env.fail("SERVICE_KEY", "must be at least 32 characters in production")
	}
}

func (env *loader) emailAddress(name, value string) {
	if value == "" {
		return
	}
	_, err := mail.ParseAddress(value)
	if err != nil {
		env.fail(name, "must be an email address such as VetMiMi <hello@example.com>")
	}
}

func (env *loader) hostPort(name, value string) {
	if value == "" {
		return
	}
	_, port, err := net.SplitHostPort(value)
	_, ok := parsePort(port)
	if err != nil || !ok {
		env.fail(name, "must be host:port")
	}
}

func parsePort(value string) (int, bool) {
	port, err := strconv.Atoi(value)
	return port, err == nil && port >= 1 && port <= 65535
}
