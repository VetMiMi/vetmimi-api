package comms

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/resend/resend-go/v2"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// sendTimeout bounds one call to Resend, which runs while the row is locked.
const sendTimeout = 30 * time.Second

// NewResend returns a Resend client for apiKey, or nil when apiKey is empty,
// as it is in development. baseURL, when set, replaces Resend's address;
// tests point it at a fake server.
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

// send hands email to Resend and returns its message id. Without a client it
// only logs what it would send: the kind and row id, never the recipient.
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

// errorCode is what a failed send records and logs. Resend's own message may
// quote the recipient, so it is never kept.
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
