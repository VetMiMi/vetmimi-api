package comms

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
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
func (t *Tasks) sendResend(ctx context.Context, row db.Communication, email Email) (string, error) {
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

// errorCode replaces the provider's error text, which may quote the recipient.
func errorCode(err error) string {
	var smtpErr *textproto.Error
	var netErr net.Error
	switch {
	case errors.As(err, &smtpErr):
		return fmt.Sprintf("smtp_%d", smtpErr.Code)
	case errors.Is(err, resend.ErrRateLimit):
		return "provider_rate_limited"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "provider_timeout"
	default:
		return "provider_error"
	}
}
