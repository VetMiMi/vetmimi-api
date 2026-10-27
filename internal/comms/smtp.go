package comms

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// SMTP sends through a mail server such as Gmail, signed in with an app password.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
}

// send returns the Message-ID. SMTP has no idempotency key, so a timeout after
// the server accepted the message can send it twice.
func (s *SMTP) send(ctx context.Context, from string, row db.Communication, email Email, now time.Time) (string, error) {
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return "", err
	}
	_, domain, _ := strings.Cut(sender.Address, "@")
	messageID := fmt.Sprintf("<%s@%s>", row.ID.String(), domain)
	msg, err := buildMessage(sender, row.Recipient.String, messageID, email, now)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return "", err
	}

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return "", err
		}
	}
	// PlainAuth refuses to send the password without TLS, except to localhost.
	if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
		return "", err
	}
	if err := c.Mail(sender.Address); err != nil {
		return "", err
	}
	if err := c.Rcpt(row.Recipient.String); err != nil {
		return "", err
	}
	w, err := c.Data()
	if err != nil {
		return "", err
	}
	if _, err := w.Write(msg); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	// The server has accepted the message; a failed QUIT must not retry it.
	_ = c.Quit()
	return messageID, nil
}

func buildMessage(from *mail.Address, to, messageID string, email Email, now time.Time) ([]byte, error) {
	var body bytes.Buffer
	parts := multipart.NewWriter(&body)
	for _, p := range []struct{ contentType, content string }{
		{"text/plain", email.Text},
		{"text/html", email.HTML},
	} {
		w, err := parts.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {p.contentType + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(p.content)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := parts.Close(); err != nil {
		return nil, err
	}

	var msg bytes.Buffer
	header := func(name, value string) { fmt.Fprintf(&msg, "%s: %s\r\n", name, value) }
	header("From", from.String())
	header("To", (&mail.Address{Address: to}).String())
	if email.ReplyTo != "" {
		header("Reply-To", (&mail.Address{Address: email.ReplyTo}).String())
	}
	header("Subject", mime.QEncoding.Encode("utf-8", email.Subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", messageID)
	header("MIME-Version", "1.0")
	header("Content-Type", "multipart/alternative; boundary="+parts.Boundary())
	msg.WriteString("\r\n")
	msg.Write(body.Bytes())
	return msg.Bytes(), nil
}
