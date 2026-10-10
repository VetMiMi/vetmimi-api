package comms

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/resend/resend-go/v2"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

const sendTimeout = 30 * time.Second

// NewResend returns nil without an apiKey, as in development.
func NewResend(apiKey, baseURL string) (*resend.Client, error) {
	if apiKey == "" {
		return nil, nil
	}
	c := resend.NewClient(apiKey)
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil {
			return nil, err
		}
		c.BaseURL = u
	}
	return c, nil
}

// The row id is the idempotency key, so a retry never sends twice.
func (t *Tasks) send(ctx context.Context, row db.Communication, email Email) (string, error) {
	if t.Resend == nil {
		t.Log.InfoContext(ctx, "would send", "kind", row.Kind, "communication_id", row.ID.String())
		return "dev", nil
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	res, err := t.Resend.Emails.SendWithOptions(ctx, &resend.SendEmailRequest{
		From:    t.From,
		To:      []string{row.Recipient.String},
		ReplyTo: email.ReplyTo,
		Subject: email.Subject,
		Text:    email.Text,
		Html:    email.HTML,
	}, &resend.SendEmailOptions{IdempotencyKey: row.ID.String()})
	if err != nil {
		return "", err
	}
	return res.Id, nil
}

// errorCode replaces Resend's error text, which may quote the recipient.
func errorCode(err error) string {
	switch {
	case errors.Is(err, resend.ErrRateLimit):
		return "provider_rate_limited"
	case errors.Is(err, context.DeadlineExceeded):
		return "provider_timeout"
	default:
		return "provider_error"
	}
}
