package assistant_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

// An editor asks for suggestions, which change nothing; a booking administrator may not.
func TestHTTP_Suggest(t *testing.T) {
	client, _, _ := newClient(t, `{"linkedin": {"text": "Reflecting on calm."}}`)
	h := &assistant.Handler{Pool: pgtest.Pool(t), Client: client}
	a := webtest.New(t, h.Routes)
	editor := webtest.SignIn(t, "content_editor")
	p, err := content.CreatePost(ctx, pgtest.Pool(t), content.Edit{Title: "Calm", Kind: "insight",
		Versions: []db.SavePostVersionParams{{Channel: "website", Enabled: true,
			Body: []byte(`{"en": "` + article + `"}`)}}}, pgtype.UUID{}, webtest.Now)
	require.NoError(t, err)
	path := "/admin/posts/" + p.ID.String() + "/suggestions"

	require.Equal(t, map[string]any{"enabled": true},
		webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/ai/status", editor, "")))
	got := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path, editor, `{"channels": ["linkedin"]}`))
	require.Equal(t, map[string]any{"linkedin": map[string]any{"text": "Reflecting on calm."}}, got)
	after, err := content.GetPost(ctx, db.New(pgtest.Pool(t)), p.ID)
	require.NoError(t, err)
	require.Equal(t, p.Version, after.Version, "nothing saved")

	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodPost, path, webtest.SignIn(t, "booking_admin"), `{"channels": ["linkedin"]}`))
	e := webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, path, editor, `{"channels": []}`))
	require.Equal(t, []any{map[string]any{"field": "/channels", "message": "must have at least 1 items"}}, e["errors"])
}
