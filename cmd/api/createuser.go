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

// userFlags are create-user's options. The password is not one: a flag would
// leave it in shell history and process listings.
type userFlags struct {
	email        string
	name         string
	roles        string
	practitioner bool
	noTOTP       bool
}

// terminal is where create-user talks to the administrator. readPassword
// reads a line without echo; tests replace it.
type terminal struct {
	in           io.Reader
	out          io.Writer
	readPassword func() ([]byte, error)
}

// The same bounds as sign-in, so every account created can sign in.
const (
	minPasswordChars = 12
	maxPasswordChars = 200
	maxEmailChars    = 254
	maxNameChars     = 120
	codeTries        = 3
)

func (flags *userFlags) register(set *flag.FlagSet) {
	set.StringVar(&flags.email, "email", "", "create-user: the administrator's email address")
	set.StringVar(&flags.name, "name", "", "create-user: the name shown in the admin")
	set.StringVar(&flags.roles, "roles", "", "create-user: comma-separated, from "+strings.Join(auth.Roles, ", "))
	set.BoolVar(&flags.practitioner, "practitioner", false, "create-user: appointments are booked with this user")
	set.BoolVar(&flags.noTOTP, "no-totp", false, "create-user: password only, no authenticator app; clears an existing user's TOTP secret")
}

// runCreateUser needs a terminal on standard input, so the password is never
// piped or echoed. On the live host: `docker compose run --rm api --mode create-user …`.
func runCreateUser(ctx context.Context, log *slog.Logger, cfg config.Config, pool *pgxpool.Pool, flags userFlags) error {
	stdin := int(os.Stdin.Fd())
	state, err := term.GetState(stdin)
	if err != nil {
		return errors.New("create-user: standard input must be a terminal")
	}
	// Ctrl-C mid-password must not leave the terminal without echo.
	defer context.AfterFunc(ctx, func() {
		// The process exits next, so a failed restore has nowhere to be reported.
		_ = term.Restore(stdin, state)
		fmt.Fprintln(os.Stderr, "\ncreate-user: interrupted")
		os.Exit(130)
	})()

	codes, err := auth.NewTOTP(cfg.TOTPEncryptionKey, time.Now)
	if err != nil {
		return err
	}
	console := terminal{
		in:           os.Stdin,
		out:          os.Stdout,
		readPassword: func() ([]byte, error) { return term.ReadPassword(stdin) },
	}
	return createUser(ctx, log, pool, codes, flags, console)
}

// createUser creates an administrator, or re-enrols one and signs them out
// everywhere. Nothing is saved until a code from the authenticator app
// matches; with --no-totp the account signs in with the password alone.
func createUser(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, codes *auth.TOTP, flags userFlags, console terminal) error {
	account, err := flags.account()
	if err != nil {
		return err
	}
	password, err := askPassword(console)
	if err != nil {
		return err
	}
	if !flags.noTOTP {
		err = enrol(codes, &account, console)
		if err != nil {
			return err
		}
	}
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	account.PasswordHash = passwordHash

	id, created, err := auth.SaveAccount(ctx, pool, account)
	if err != nil {
		return fmt.Errorf("create-user: %w", err)
	}

	outcome := "updated"
	if created {
		outcome = "created"
	}
	log.Info("user "+outcome, "user_id", id.String())
	fmt.Fprintln(console.out, outcome)
	return nil
}

// enrol shows a new TOTP secret and, once a code from the app matches, puts
// the sealed secret in account.
func enrol(codes *auth.TOTP, account *auth.Account, console terminal) error {
	enrolment, err := auth.NewEnrolment(account.Email)
	if err != nil {
		return err
	}
	fmt.Fprintf(console.out, "Add this account to an authenticator app:\n\n%s\n\n", enrolment.URI)
	step, err := confirmCode(codes, enrolment.Secret, console)
	if err != nil {
		return err
	}
	account.EnrolmentStep = step
	sealed, err := codes.Seal(enrolment.Secret)
	if err != nil {
		return err
	}
	account.SealedTOTPSecret = sealed
	return nil
}

// account checks the flags. Its errors never quote the email.
func (flags userFlags) account() (auth.Account, error) {
	email := auth.NormalizeEmail(flags.email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > maxEmailChars {
		return auth.Account{}, errors.New("create-user: --email must be a plain address such as name@example.com")
	}
	name := strings.TrimSpace(flags.name)
	if name == "" || utf8.RuneCountInString(name) > maxNameChars {
		return auth.Account{}, fmt.Errorf("create-user: --name is required, at most %d characters", maxNameChars)
	}
	roles, err := parseRoles(flags.roles)
	if err != nil {
		return auth.Account{}, err
	}
	account := auth.Account{
		Email:        email,
		DisplayName:  name,
		Roles:        roles,
		Practitioner: flags.practitioner,
	}
	return account, nil
}

func parseRoles(list string) ([]string, error) {
	var roles []string
	for _, role := range strings.Split(list, ",") {
		role = strings.TrimSpace(role)
		if !slices.Contains(auth.Roles, role) {
			return nil, errors.New("create-user: --roles takes one or more of " + strings.Join(auth.Roles, ", ") + ", comma-separated")
		}
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}
	return roles, nil
}

func askPassword(console terminal) (string, error) {
	first, err := promptPassword(console, "Password: ")
	if err != nil {
		return "", err
	}
	length := utf8.RuneCount(first)
	if length < minPasswordChars || length > maxPasswordChars {
		return "", fmt.Errorf("create-user: the password must be %d to %d characters", minPasswordChars, maxPasswordChars)
	}
	second, err := promptPassword(console, "Repeat it: ")
	if err != nil {
		return "", err
	}
	if !bytes.Equal(first, second) {
		return "", errors.New("create-user: the passwords do not match")
	}
	return string(first), nil
}

func promptPassword(console terminal, prompt string) ([]byte, error) {
	fmt.Fprint(console.out, prompt)
	password, err := console.readPassword()
	// The Enter key was not echoed either.
	fmt.Fprintln(console.out)
	if err != nil {
		return nil, fmt.Errorf("create-user: read password: %w", err)
	}
	return password, nil
}

// confirmCode returns the step of the first valid code typed.
func confirmCode(codes *auth.TOTP, secret []byte, console terminal) (int64, error) {
	lines := bufio.NewScanner(console.in)
	for range codeTries {
		fmt.Fprint(console.out, "Code from the app: ")
		if !lines.Scan() {
			return 0, errors.New("create-user: no code entered; nothing was saved")
		}
		step, ok := codes.Match(secret, strings.TrimSpace(lines.Text()))
		if ok {
			return step, nil
		}
		fmt.Fprintln(console.out, "That code is not valid.")
	}
	return 0, fmt.Errorf("create-user: %d wrong codes; nothing was saved", codeTries)
}
