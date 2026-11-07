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
func (mailer *SMTP) send(ctx context.Context, from string, row db.Communication, email Email, now time.Time) (string, error) {
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return "", err
	}
	_, domain, _ := strings.Cut(sender.Address, "@")
	messageID := fmt.Sprintf("<%s@%s>", row.ID.String(), domain)
	message, err := buildMessage(sender, row.Recipient.String, messageID, email, now)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(mailer.Host, strconv.Itoa(mailer.Port)))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	err = conn.SetDeadline(deadline)
	if err != nil {
		return "", err
	}

	client, err := smtp.NewClient(conn, mailer.Host)
	if err != nil {
		return "", err
	}
	defer client.Close()
	hasTLS, _ := client.Extension("STARTTLS")
	if hasTLS {
		err = client.StartTLS(&tls.Config{ServerName: mailer.Host})
		if err != nil {
			return "", err
		}
	}
	// PlainAuth refuses to send the password without TLS, except to localhost.
	err = client.Auth(smtp.PlainAuth("", mailer.Username, mailer.Password, mailer.Host))
	if err != nil {
		return "", err
	}
	err = client.Mail(sender.Address)
	if err != nil {
		return "", err
	}
	err = client.Rcpt(row.Recipient.String)
	if err != nil {
		return "", err
	}
	writer, err := client.Data()
	if err != nil {
		return "", err
	}
	_, err = writer.Write(message)
	if err != nil {
		return "", err
	}
	err = writer.Close()
	if err != nil {
		return "", err
	}
	// The server has accepted the message; a failed QUIT must not retry it.
	_ = client.Quit()
	return messageID, nil
}

func buildMessage(from *mail.Address, to, messageID string, email Email, now time.Time) ([]byte, error) {
	var body bytes.Buffer
	parts := multipart.NewWriter(&body)
	for _, part := range []struct{ contentType, content string }{
		{"text/plain", email.Text},
		{"text/html", email.HTML},
	} {
		writer, err := parts.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.contentType + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		encoder := quotedprintable.NewWriter(writer)
		_, err = encoder.Write([]byte(part.content))
		if err != nil {
			return nil, err
		}
		err = encoder.Close()
		if err != nil {
			return nil, err
		}
	}
	err := parts.Close()
	if err != nil {
		return nil, err
	}

	var message bytes.Buffer
	header := func(name, value string) { fmt.Fprintf(&message, "%s: %s\r\n", name, value) }
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
	message.WriteString("\r\n")
	message.Write(body.Bytes())
	return message.Bytes(), nil
}
