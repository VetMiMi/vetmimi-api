# Style

One standard for every package, so a reader who knows one feature knows them
all. When this file and the code disagree, the code is wrong.

`internal/content` is the reference: when in doubt, copy what it does.

## 1. Folders

```
cmd/api/                start-up: config, connections, wiring, the four modes
internal/web/           what every route shares: router, auth, limits, binding, errors
internal/queue/         background tasks: queue, worker, schedules
internal/db/queries/    SQL, written by hand
internal/db/            Go generated from the SQL by sqlc (never edited)
internal/<feature>/     one folder per feature: its routes, rules and tasks
migrations/             database changes, never edited after merge
```

No `controllers/`, `services/`, `repositories/`, `utils/` or `helpers/`.

## 2. Files inside a feature

| File | Holds | Nothing else |
|---|---|---|
| `handler.go` | the `Handler` struct and `Routes()` | no handlers, no helpers |
| `http_<area>.go` | request structs, response structs, handlers, response builders | no SQL, no business rules |
| `<topic>.go` | business rules: types, errors, exported operations, private helpers | no gin, no JSON tags for requests |
| `worker.go` | task names, payloads, `Tasks`, `Register()`, task handlers, task builders, sweep | no HTTP |
| `*_test.go` | tests named after the file they test | |

A feature without background tasks has no `worker.go`. A file passes 350 lines
→ split it by topic (`posts.go`, `workflow.go`), never by kind (`types.go`).

### Order inside each file

Every file reads top to bottom in the same order. Leave out a section that
does not exist; never add an empty one.

- **handler.go:** imports → `Handler` struct → rate-limit rules → `Routes()`.
- **http_*.go:** imports → path structs → query structs → body structs →
  response structs → handlers (in the order `Routes()` lists them) →
  `newXResponse` builders → small helpers.
- **domain files:** package doc (first file only) → imports → types → constants
  → errors → exported operations → private helpers.
- **worker.go:** task names → constants → payloads → `Tasks` struct →
  `Register()` → task handlers → task builders → sweep → helpers.

## 3. Names

| Thing | Name | Example |
|---|---|---|
| path struct (non-id params) | `<thing>Path` | `articlePath`, `channelPath` |
| query struct | `<action>Query` | `listPostsQuery` |
| JSON body struct | `<action>Body` | `createPostBody`, `scheduleBody` |
| response struct | `<thing>Response` | `postResponse` |
| response builder | `new<Thing>Response` | `newPostResponse(post)` |
| handler method | verb + noun | `createPost`, `retryChannel` |
| domain operation | Verb + Noun, exported | `CreatePost`, `Schedule` |
| task handler | named after the task, exported | `PublishChannel` |
| task builder | `<Kind>Task(s)` | `PublishTasks(post)` |

Variables use the whole word: `post`, `posts`, `version`, `publication`,
`edit`, `filter`, `body`, `query`, `path`, `response`, `result`.

These short names are kept because every Go reader knows them, and are used
for nothing else: `ctx` context, `err` error, `c` gin context, `h` Handler,
`t` Tasks, `w` worker, `r` router, `q` queries, `tx` transaction, `i` index.

## 4. A handler

Every handler has the same four blocks, separated by blank lines:

1. **Read** the request: path, then query, then body. End with `ctx := c.Request.Context()`.
2. **Do** the work: build the domain input, call one domain function, handle its error.
3. **After the commit**: queue tasks, log. Only after the call succeeded.
4. **Answer**: `c.JSON(status, newXResponse(result))`.

```go
func (h *Handler) schedulePost(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	var body scheduleBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	post, err := Schedule(ctx, h.Pool, postID, body.Version, body.ScheduledAt, currentUserID(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.Queue.Enqueue(ctx, ScheduledTask(post)...)
	logPost(ctx, "post_scheduled", post)
	c.JSON(http.StatusOK, newPostResponse(post))
}
```

Reading input, one way each:

| Input | How |
|---|---|
| an id in the path | `id, ok := web.PathUUID(c, "postId")` |
| any other path value | a `<thing>Path` struct and `web.BindURI` |
| query string | a `<action>Query` struct and `web.BindQuery` |
| JSON body | a `<action>Body` struct and `web.BindJSON` |

All four answer 400 with the bad field named; the handler only returns.

- Errors go to `web.Error(c, err)`. Never write error JSON by hand.
- Who may call, and rate limits, are set in `Routes()`, never in the handler.
- Logs hold ids and statuses, never names, emails, notes or post text.

## 5. A domain operation

Domain functions take `ctx`, then the database, then ids, then input, then the
acting user, then `now`. They return a result and an `apperr` error.

- **One query** (a read, or a single write): take `q db.Querier`. The handler
  passes `db.New(h.Pool)`.
- **More than one write, or a read that decides a write**: take
  `pool *pgxpool.Pool` and open the transaction yourself, in plain sight:

```go
func Unschedule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, id, version)
	if err != nil {
		return Post{}, err
	}
	err = unschedule.check(&post)
	if err != nil {
		return Post{}, err
	}
	backToDraft(&post)
	result, err := savePost(ctx, q, post, now)
	if err != nil {
		return Post{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Post{}, err
	}
	return result, nil
}
```

`Begin` → `defer tx.Rollback(ctx)` → `q := db.New(tx)` → the work → `Commit`.
Rollback after a commit does nothing, so its error is not checked. No
`pgx.BeginFunc`, no "run this callback inside a transaction" helpers: a
reader must see where the transaction starts and ends.

- Turn a PostgreSQL refusal into an `apperr` right where the query runs
  (`asSlugTaken(err)`), so a reader sees which statement can fail that way.
- Never queue a task inside a transaction. Return the result; the caller
  queues after the commit.

## 6. A worker

```go
const TaskPublishChannel = "content:publish-channel"

type publishPayload struct {
	PostID  string `json:"postId"`
	Channel string `json:"channel,omitempty"`
}

type Tasks struct {
	Pool  *pgxpool.Pool
	Queue *queue.Queue
	Log   *slog.Logger
	Now   clock.Now
}

func (t *Tasks) Register(w *queue.Worker) {
	w.Handle(TaskPublishChannel, t.PublishChannel)
	w.Handle(TaskSweep, t.Sweep)
	w.Every("@every 5m", TaskSweep)
}

// PublishChannel is safe to repeat: a channel is claimed once per attempt.
func (t *Tasks) PublishChannel(ctx context.Context, payload []byte) error { ... }
```

- Each task handler says in its doc comment **why it is safe to run twice**.
- Return an error only when asynq should try again.
- Every task that matters has a **sweep** that rebuilds it from PostgreSQL, so a
  task Redis lost is never lost for good. Keep the sweeps even when they look
  repetitive.
- Task ids make duplicates impossible: `fmt.Sprintf("post:%s:%d", id, at.Unix())`.

## 7. Writing Go

**One step per line.** Call, then check:

```go
// do
err = saveVersions(ctx, q, post.ID, edit.Versions, now)
if err != nil {
	return Post{}, err
}

// don't
if err := saveVersions(ctx, q, post.ID, edit.Versions, now); err != nil {
```

**Name a value before using it** when building it takes more than one line:

```go
// do
bookingHandler := &booking.Handler{
	Pool:  pool,
	Queue: tasks,
	Now:   time.Now,
}
bookingHandler.Routes(routes)

// don't
(&booking.Handler{Pool: pool, Queue: tasks, Now: time.Now}).Routes(routes)
```

**One field per line** in a struct literal with more than two fields.

**One assignment per line**: `post.Status = "draft"` then `post.ScheduledAt = ...`,
never `a, b = x, y` for unrelated values.

**Early returns.** Handle the failure and return; the success path stays at the
left margin. No `else` after a `return`.

**No `_ = err`.** Check every error. The only exceptions are `defer
tx.Rollback(ctx)`, `defer res.Body.Close()`, and a value that cannot fail,
which gets a comment saying so.

**Comments** only when the name cannot say it: one line, saying why. No
narration, no references to ADRs or issues.

## 8. Dependencies

- A feature's dependencies are fields on its `Handler` or `Tasks` struct, set
  in `cmd/api`. No globals, no constructors that hide what is set.
- `Now clock.Now` instead of calling `time.Now()` in rules, so tests fix the time.
- A nil `*queue.Queue` does nothing (tests use this); call `h.Queue.Enqueue`,
  `Replace` and `Remove` directly, with no nil check.

## 9. Keep it simple

- No interface with one implementation. No generics. No option structs.
- No helper with one caller unless it gives a long block a clear name.
- No wrapper that only renames a call.
- Two operations that only look alike stay separate functions.
- Standard library first; a new dependency must remove real code.

## 10. Tests

- Rules are tested by calling the functions against real PostgreSQL.
- Handlers are tested through `internal/webtest`: real router and middleware,
  checking status, JSON shape and error codes.
- Every behaviour: one success test, one failure test. Scheduling code also
  gets a concurrency test.
