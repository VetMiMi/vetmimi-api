package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/config"
)

// The password bounds are SessionCreate's in openapi.yaml, so every password
// create-user accepts can be used to sign in. The email and name caps are
// the API's own input caps.
const (
	minPasswordChars = 12
	maxPasswordChars = 200
	maxEmailChars    = 254
	maxNameChars     = 120
	codeTries        = 3
)

// userFlags are create-user's options. The password is deliberately not one
// of them: a flag or an environment variable would leave it in shell history
// and process listings.
type userFlags struct {
	email        string
	name         string
	roles        string
	practitioner bool
	noTOTP       bool
}

func (f *userFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.email, "email", "", "create-user: the administrator's email address")
	fs.StringVar(&f.name, "name", "", "create-user: the name shown in the admin")
	fs.StringVar(&f.roles, "roles", "", "create-user: comma-separated, from "+strings.Join(auth.Roles, ", "))
	fs.BoolVar(&f.practitioner, "practitioner", false, "create-user: appointments are booked with this user")
	fs.BoolVar(&f.noTOTP, "no-totp", false, "create-user: password only, no authenticator app; clears an existing user's TOTP secret")
}

// terminal is where create-user talks to the administrator. readPassword
// reads one line without echo: term.ReadPassword in production, a plain
// reader in tests.
type terminal struct {
	in           io.Reader
	out          io.Writer
	readPassword func() ([]byte, error)
}

// runCreateUser wires create-user to the process's terminal. Standard input
// must be a terminal, so the password is never piped or echoed; on the live
// host that is `docker compose run --rm api --mode create-user …`.
func runCreateUser(ctx context.Context, log *slog.Logger, cfg config.Config, pool *pgxpool.Pool, f userFlags) error {
	fd := int(os.Stdin.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return errors.New("create-user: standard input must be a terminal")
	}
	// Ctrl-C would otherwise only cancel ctx while a read blocks, and killing
	// the process mid-password would leave the terminal without echo.
	defer context.AfterFunc(ctx, func() {
		_ = term.Restore(fd, state)
		fmt.Fprintln(os.Stderr, "\ncreate-user: interrupted")
		os.Exit(130)
	})()

	codes, err := auth.NewTOTP(cfg.TOTPEncryptionKey, time.Now)
	if err != nil {
		return err
	}
	return createUser(ctx, log, pool, codes, f, terminal{
		in:           os.Stdin,
		out:          os.Stdout,
		readPassword: func() ([]byte, error) { return term.ReadPassword(fd) },
	})
}

// createUser creates an administrator, or re-enrols an existing one and signs
// them out everywhere. It saves nothing until the administrator proves the
// authenticator app holds the new secret by typing a code from it. With
// --no-totp there is no authenticator step: the account signs in with the
// password alone until two-step setup moves into the admin website (#142).
func createUser(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, codes *auth.TOTP, f userFlags, t terminal) error {
	account, err := f.account()
	if err != nil {
		return err
	}
	password, err := askPassword(t)
	if err != nil {
		return err
	}
	if !f.noTOTP {
		if err := enrol(codes, &account, t); err != nil {
			return err
		}
	}
	if account.PasswordHash, err = auth.HashPassword(password); err != nil {
		return err
	}
	id, created, err := auth.SaveAccount(ctx, pool, account)
	if err != nil {
		return fmt.Errorf("create-user: %w", err)
	}
	outcome := "updated"
	if created {
		outcome = "created"
	}
	log.Info("user "+outcome, "user_id", id.String())
	fmt.Fprintln(t.out, outcome)
	return nil
}

// enrol shows a new TOTP secret, waits for a valid code from the app and
// puts the sealed secret and the code's step in account.
func enrol(codes *auth.TOTP, account *auth.Account, t terminal) error {
	enrolment, err := auth.NewEnrolment(account.Email)
	if err != nil {
		return err
	}
	fmt.Fprintf(t.out, "Add this account to an authenticator app:\n\n%s\n\n", enrolment.URI)
	if account.EnrolmentStep, err = confirmCode(codes, enrolment.Secret, t); err != nil {
		return err
	}
	account.SealedTOTPSecret, err = codes.Seal(enrolment.Secret)
	return err
}

// account checks the flags. Its errors never quote the email.
func (f userFlags) account() (auth.Account, error) {
	email := auth.NormalizeEmail(f.email)
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > maxEmailChars {
		return auth.Account{}, errors.New("create-user: --email must be a plain address such as name@example.com")
	}
	name := strings.TrimSpace(f.name)
	if name == "" || utf8.RuneCountInString(name) > maxNameChars {
		return auth.Account{}, fmt.Errorf("create-user: --name is required, at most %d characters", maxNameChars)
	}
	roles, err := parseRoles(f.roles)
	if err != nil {
		return auth.Account{}, err
	}
	return auth.Account{Email: email, DisplayName: name, Roles: roles, Practitioner: f.practitioner}, nil
}

func parseRoles(list string) ([]string, error) {
	var roles []string
	for _, r := range strings.Split(list, ",") {
		r = strings.TrimSpace(r)
		if !slices.Contains(auth.Roles, r) {
			return nil, errors.New("create-user: --roles takes one or more of " + strings.Join(auth.Roles, ", ") + ", comma-separated")
		}
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	return roles, nil
}

func askPassword(t terminal) (string, error) {
	first, err := promptPassword(t, "Password: ")
	if err != nil {
		return "", err
	}
	if n := utf8.RuneCount(first); n < minPasswordChars || n > maxPasswordChars {
		return "", fmt.Errorf("create-user: the password must be %d to %d characters", minPasswordChars, maxPasswordChars)
	}
	second, err := promptPassword(t, "Repeat it: ")
	if err != nil {
		return "", err
	}
	if !bytes.Equal(first, second) {
		return "", errors.New("create-user: the passwords do not match")
	}
	return string(first), nil
}

func promptPassword(t terminal, prompt string) ([]byte, error) {
	fmt.Fprint(t.out, prompt)
	password, err := t.readPassword()
	// The administrator's Enter was not echoed either.
	fmt.Fprintln(t.out)
	if err != nil {
		return nil, fmt.Errorf("create-user: read password: %w", err)
	}
	return password, nil
}

// confirmCode returns the step of the first valid code typed.
func confirmCode(codes *auth.TOTP, secret []byte, t terminal) (int64, error) {
	lines := bufio.NewScanner(t.in)
	for range codeTries {
		fmt.Fprint(t.out, "Code from the app: ")
		if !lines.Scan() {
			return 0, errors.New("create-user: no code entered; nothing was saved")
		}
		if step, ok := codes.Match(secret, strings.TrimSpace(lines.Text())); ok {
			return step, nil
		}
		fmt.Fprintln(t.out, "That code is not valid.")
	}
	return 0, fmt.Errorf("create-user: %d wrong codes; nothing was saved", codeTries)
}
