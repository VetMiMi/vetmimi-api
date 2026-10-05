package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func TestCleanupDeletesExpiredSessions(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	ctx := context.Background()
	userID, err := q.CreateUser(ctx, db.CreateUserParams{
		Email: newFlags(t).email, DisplayName: "Mi", PasswordHash: "$argon2id$x",
		Roles: []string{"site_admin"}, TotpSecretEnc: []byte("sealed"),
	})
	require.NoError(t, err)
	session := func(name string, signedIn time.Time) pgtype.UUID {
		id, err := q.CreateSession(ctx, db.CreateSessionParams{
			UserID: userID, TokenHash: []byte(name), Now: signedIn, ExpiresAt: signedIn.Add(auth.SessionLifetime),
		})
		require.NoError(t, err)
		return id
	}
	live := session("live", now.Add(-time.Hour))
	idle := session("idle", now.Add(-auth.SessionIdle))

	var logs bytes.Buffer
	require.NoError(t, cleanup(ctx, slog.New(slog.NewJSONHandler(&logs, nil)), q, now))

	var left []pgtype.UUID
	rows, err := pgtest.Pool(t).Query(ctx, "SELECT id FROM sessions WHERE user_id = $1", userID)
	require.NoError(t, err)
	for rows.Next() {
		var id pgtype.UUID
		require.NoError(t, rows.Scan(&id))
		left = append(left, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []pgtype.UUID{live}, left)
	require.NotContains(t, left, idle)
	require.Contains(t, logs.String(), `"msg":"expired rows deleted"`)
}
