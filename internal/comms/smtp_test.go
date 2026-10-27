package comms_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
)

func TestDeliver_SendsThroughSMTP(t *testing.T) {
	appt := newAppointment(t, freeStart())
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "my"})
	server := newFakeSMTP(t, "app-password")
	tasks, logs := newTasks(t, nil, time.Now())
	tasks.SMTP = server.sender("app-password")

	require.NoError(t, deliver(t, tasks, task))

	sent := server.sent()
	require.Len(t, sent, 1)
	require.Equal(t, "hello@example.com", sent[0].from)
	require.Equal(t, visitorEmail, sent[0].to)
	msg, err := mail.ReadMessage(bytes.NewReader(sent[0].data))
	require.NoError(t, err)
	require.Equal(t, `"VetMiMi" <hello@example.com>`, msg.Header.Get("From"))
	require.Equal(t, "<meenaerie@gmail.com>", msg.Header.Get("Reply-To"))
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(subject, "အတည်ပြုပြီးပါပြီ"), "the Burmese subject survives encoding")

	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	require.NoError(t, err)
	parts := multipart.NewReader(msg.Body, params["boundary"])
	for _, want := range []string{"text/plain; charset=utf-8", "text/html; charset=utf-8"} {
		p, err := parts.NextPart()
		require.NoError(t, err)
		require.Equal(t, want, p.Header.Get("Content-Type"))
		body, err := io.ReadAll(p)
		require.NoError(t, err)
		require.NotEmpty(t, body)
	}

	got := row(t, r.ID)
	require.Equal(t, "sent", got.Status)
	require.Equal(t, "<"+r.ID.String()+"@example.com>", got.ProviderMessageID.String)
	require.Equal(t, got.ProviderMessageID.String, msg.Header.Get("Message-ID"))
	require.NotContains(t, logs.String(), visitorEmail)
}

func TestDeliver_RecordsTheSMTPRefusal(t *testing.T) {
	appt := newAppointment(t, freeStart())
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.RequestReceived, Recipient: visitorEmail, Locale: "en"})
	server := newFakeSMTP(t, "app-password")
	tasks, logs := newTasks(t, nil, time.Now())
	tasks.SMTP = server.sender("wrong-password")

	for range 4 {
		require.Error(t, deliver(t, tasks, task), "a refused sign-in is retried")
	}
	require.NoError(t, deliver(t, tasks, task), "the last attempt ends the retries")

	got := row(t, r.ID)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "smtp_535", got.Error.String)
	require.Empty(t, server.sent())
	require.NotContains(t, logs.String(), visitorEmail)
	require.NotContains(t, logs.String(), "wrong-password")
}

// fakeSMTP speaks just enough SMTP for net/smtp: no TLS, which PlainAuth allows on 127.0.0.1.
type fakeSMTP struct {
	addr     *net.TCPAddr
	password string

	mu       sync.Mutex
	messages []smtpMessage
}

type smtpMessage struct {
	from, to string
	data     []byte
}

func newFakeSMTP(t *testing.T, password string) *fakeSMTP {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	f := &fakeSMTP{addr: l.Addr().(*net.TCPAddr), password: password}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) sender(password string) *comms.SMTP {
	return &comms.SMTP{Host: "127.0.0.1", Port: f.addr.Port, Username: "hello@example.com", Password: password}
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	c := textproto.NewConn(conn)
	var m smtpMessage
	_ = c.PrintfLine("220 fake ESMTP")
	for {
		line, err := c.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			_ = c.PrintfLine("250-fake")
			_ = c.PrintfLine("250 AUTH PLAIN")
		case "AUTH":
			_, encoded, _ := strings.Cut(arg, " ")
			creds, _ := base64.StdEncoding.DecodeString(encoded)
			if string(creds) == "\x00hello@example.com\x00"+f.password {
				_ = c.PrintfLine("235 2.7.0 accepted")
			} else {
				_ = c.PrintfLine("535 5.7.8 bad credentials")
			}
		case "MAIL":
			m.from = strings.Trim(strings.TrimPrefix(arg, "FROM:"), "<>")
			_ = c.PrintfLine("250 ok")
		case "RCPT":
			m.to = strings.Trim(strings.TrimPrefix(arg, "TO:"), "<>")
			_ = c.PrintfLine("250 ok")
		case "DATA":
			_ = c.PrintfLine("354 go ahead")
			m.data, _ = c.ReadDotBytes()
			f.mu.Lock()
			f.messages = append(f.messages, m)
			f.mu.Unlock()
			_ = c.PrintfLine("250 queued")
		case "QUIT":
			_ = c.PrintfLine("221 bye")
			return
		default:
			_ = c.PrintfLine("250 ok")
		}
	}
}

func (f *fakeSMTP) sent() []smtpMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]smtpMessage(nil), f.messages...)
}
