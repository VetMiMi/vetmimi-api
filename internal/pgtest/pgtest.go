// Package pgtest gives each test binary its own migrated PostgreSQL database,
// vetmimi_test_<random>, on the DATABASE_URL_TEST server. A package using it
// calls it from TestMain: func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/postgres"
)

const envURL = "DATABASE_URL_TEST"

var (
	running bool

	once     sync.Once
	shared   *pgxpool.Pool
	sharedDB string
	setupErr error
)

// Run runs the package's tests, then closes the shared pool and drops its
// database. It returns the exit code for os.Exit.
func Run(m *testing.M) int {
	running = true
	code := m.Run()
	if err := teardown(); err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// Pool returns a pool on this test binary's migrated database. Every call in
// one package shares the database, so tests must not assume empty tables.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	server := serverURL(t)
	once.Do(func() {
		sharedDB, setupErr = createDatabase(server)
		if setupErr != nil {
			return
		}
		ctx := context.Background()
		shared, setupErr = postgres.Open(ctx, sharedDB)
		if setupErr == nil {
			setupErr = postgres.Migrate(ctx, shared)
		}
	})
	if setupErr != nil {
		t.Fatal("pgtest: ", setupErr)
	}
	return shared
}

// EmptyDatabase returns the URL of a new database with no migrations
// applied, dropped when the test ends. It is for tests of migration itself.
func EmptyDatabase(t testing.TB) string {
	t.Helper()
	dbURL, err := createDatabase(serverURL(t))
	if err != nil {
		t.Fatal("pgtest: ", err)
	}
	t.Cleanup(func() {
		if err := dropDatabase(dbURL); err != nil {
			t.Error("pgtest: ", err)
		}
	})
	return dbURL
}

// serverURL fails rather than skips without a server: a skipped database
// test reads as a pass, and these tests guard booking correctness.
func serverURL(t testing.TB) string {
	t.Helper()
	if !running {
		t.Fatal("pgtest: call pgtest.Run from TestMain, or the test database is never dropped")
	}
	server := os.Getenv(envURL)
	if server == "" {
		t.Fatal("pgtest: " + envURL + " is not set; point it at a PostgreSQL server where this user may create databases, " +
			"for example postgres://localhost:5432/vetmimi_test?sslmode=disable")
	}
	return server
}

func createDatabase(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Scheme == "" {
		return "", errors.New(envURL + " must be a postgres:// URL")
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	name := "vetmimi_test_" + hex.EncodeToString(random)
	if err := admin(server, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}

func dropDatabase(dbURL string) error {
	u, err := url.Parse(dbURL)
	if err != nil {
		return err
	}
	name := pgx.Identifier{u.Path[1:]}.Sanitize()
	// FORCE ends connections a test left open, such as a pool it never closed.
	return admin(dbURL, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
}

func teardown() error {
	if sharedDB == "" {
		return nil
	}
	if shared != nil {
		shared.Close()
	}
	return dropDatabase(sharedDB)
}

// admin runs one statement on the server's maintenance database, because a
// database cannot be created or dropped from a connection to itself.
func admin(server, sql string) error {
	cfg, err := pgx.ParseConfig(server)
	if err != nil {
		return errors.New(envURL + " is not a valid connection URL")
	}
	cfg.Database = "postgres"
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}
