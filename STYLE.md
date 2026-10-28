# Style

How code in this repository is written. The test for every file: **could you
explain it to someone in two minutes?** If not, it is too clever.

## Layout: one folder per feature

```
cmd/api/main.go          wiring: config, database, Redis, handlers, routes
internal/web/            what every route shares: router, auth, limits, errors, binding
internal/booking/
  handler.go             Handler struct and Routes(): every booking route in one list
  http_public.go         handlers for the website's booking pages
  http_appointments.go   handlers for the admin's appointment screens
  requests.go            the rules: what makes a request valid, what it stores
  ...                    more rule files, one per feature (slots.go, reschedule.go)
internal/db/             SQL queries (queries/*.sql) and the Go sqlc generates from them
```

A feature's routes, handlers and rules live together. There are no
`controllers/`, `services/` or `repositories/` folders.

## The three layers, as files

| Role | Where | Does |
|---|---|---|
| Handler | `http_*.go` | Bind the request, call one function, write JSON |
| Rules | the feature's other files | Plain functions: `ctx`, the pool, inputs → result or `apperr` |
| Queries | `internal/db/queries/*.sql` | SQL; `sqlc` writes the Go |

## A handler

```go
type localeQuery struct {
	Locale string `form:"locale" binding:"omitempty,oneof=en my"`
}

func (h *Handler) listPublicServices(c *gin.Context) {
	var q localeQuery
	if !web.BindQuery(c, &q) {
		return
	}
	list, err := ListPublicServices(c.Request.Context(), db.New(h.Pool), cmp.Or(q.Locale, "en"))
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}
```

- **Request structs are written by hand** next to the handler, with `json` and
  `binding` tags. `web.BindJSON` / `web.BindQuery` answer 400 with each bad
  field named.
- **Response structs are written by hand** too. When a domain struct already
  has the right shape, give it `json` tags and return it; otherwise write a
  small `fooResponse` struct beside the handler.
- **Errors**: rules return `apperr` errors (`apperr.New(apperr.NotFound, "...")`).
  `web.Error` turns any error into the problem JSON the site reads. Never write
  error JSON by hand.
- **Who may call**: the route says it, in `Routes()`:
  `r.Admin(auth.BookingAdmin).POST("/admin/services", h.createService)`.
  `r.Admin` and `r.Slow` take the roles, so no admin route can forget them.
- **Rate limits**: also in `Routes()`: `r.Public.POST(path, r.Limit(rule), h.fn)`.

## Rules for every file

- Short functions, early returns, plain names. No interfaces with one
  implementation, no generics, no option structs for needs that do not exist.
- Comments only when the name cannot say it: one line, saying **why**.
- No dead code. Delete what a change leaves unused.
- Logs never hold names, emails, notes or tokens.

## Tests

- Rules are tested by calling the functions against real PostgreSQL.
- Handlers are tested through `internal/webtest`: real router, real middleware,
  checking status, JSON shape and error codes, not the rules again.
- Every behaviour: one success test, one failure test.
