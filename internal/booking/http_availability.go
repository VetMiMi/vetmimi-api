package booking

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type ruleInput struct {
	Weekday   *int16 `json:"weekday" binding:"required,min=1,max=7"`
	StartTime string `json:"startTime" binding:"required,hhmm"`
	EndTime   string `json:"endTime" binding:"required,hhmm"`
}

type weeklyHoursInput struct {
	Periods []ruleInput `json:"periods" binding:"required,max=70,dive"`
}

// Dates and times are bound as strings so that a bad one is reported by its field name.
type overrideInput struct {
	OnDate   string  `json:"onDate" binding:"required,datetime=2006-01-02"`
	Kind     string  `json:"kind" binding:"required,oneof=open replace"`
	StartsAt string  `json:"startsAt" binding:"required,datetime=2006-01-02T15:04:05Z07:00"`
	EndsAt   string  `json:"endsAt" binding:"required,datetime=2006-01-02T15:04:05Z07:00"`
	Note     *string `json:"note" binding:"omitnil,max=500"`
}

type blockInput struct {
	StartsAt string  `json:"startsAt" binding:"required,datetime=2006-01-02T15:04:05Z07:00"`
	EndsAt   string  `json:"endsAt" binding:"required,datetime=2006-01-02T15:04:05Z07:00"`
	AllDay   bool    `json:"allDay"`
	Reason   *string `json:"reason" binding:"omitnil,max=500"`
}

// dateRangeQuery is optional local dates; the rules default to the next 62 days.
type dateRangeQuery struct {
	From string `form:"from" binding:"omitempty,datetime=2006-01-02"`
	To   string `form:"to" binding:"omitempty,datetime=2006-01-02"`
}

type previewQuery struct {
	ServiceID string `form:"serviceId" binding:"required"`
	From      string `form:"from" binding:"required,datetime=2006-01-02"`
	To        string `form:"to" binding:"required,datetime=2006-01-02"`
}

type ruleJSON struct {
	ID        pgtype.UUID `json:"id"`
	Weekday   int16       `json:"weekday"`
	StartTime string      `json:"startTime"`
	EndTime   string      `json:"endTime"`
}

type overrideJSON struct {
	ID        pgtype.UUID `json:"id"`
	OnDate    string      `json:"onDate"`
	Kind      string      `json:"kind"`
	StartsAt  time.Time   `json:"startsAt"`
	EndsAt    time.Time   `json:"endsAt"`
	Note      *string     `json:"note,omitempty"`
	CreatedAt time.Time   `json:"createdAt"`
}

type blockJSON struct {
	ID        pgtype.UUID `json:"id"`
	StartsAt  time.Time   `json:"startsAt"`
	EndsAt    time.Time   `json:"endsAt"`
	AllDay    bool        `json:"allDay"`
	Reason    *string     `json:"reason,omitempty"`
	CreatedAt time.Time   `json:"createdAt"`
}

type savedBlockJSON struct {
	Block     blockJSON      `json:"block"`
	Conflicts []conflictJSON `json:"conflicts"`
}

// conflictJSON is an appointment summary: the visitor's name, never their email or notes.
type conflictJSON struct {
	ID              pgtype.UUID    `json:"id"`
	Reference       string         `json:"reference"`
	Status          string         `json:"status"`
	Service         serviceRefJSON `json:"service"`
	StartsAt        time.Time      `json:"startsAt"`
	EndsAt          time.Time      `json:"endsAt"`
	DurationMinutes int32          `json:"durationMinutes"`
	Timezone        string         `json:"timezone"`
	Format          string         `json:"format"`
	Source          string         `json:"source"`
	VisitorName     string         `json:"visitorName"`
	HoldExpiresAt   *time.Time     `json:"holdExpiresAt,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

type serviceRefJSON struct {
	ID   pgtype.UUID     `json:"id"`
	Slug string          `json:"slug"`
	Name json.RawMessage `json:"name"`
}

type previewResponse struct {
	ServiceID string        `json:"serviceId"`
	Timezone  string        `json:"timezone"`
	From      string        `json:"from"`
	To        string        `json:"to"`
	Days      []slotDayJSON `json:"days"`
}

func (h *Handler) listRules(c *gin.Context) {
	ctx := c.Request.Context()
	q := db.New(h.Pool)
	tz, _, err := PracticeZone(ctx, q)
	if err != nil {
		web.Error(c, err)
		return
	}
	rows, err := ListRules(ctx, q)
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"timezone": tz, "items": rulesOf(rows)})
}

func (h *Handler) createRule(c *gin.Context) {
	var in ruleInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	r, err := CreateRule(ctx, h.Pool, in.rule())
	if err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_rule_created", r.ID)
	c.JSON(http.StatusCreated, ruleOf(r))
}

func (h *Handler) updateRule(c *gin.Context) {
	id, ok := web.PathUUID(c, "ruleId")
	if !ok {
		return
	}
	var in ruleInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	r, err := UpdateRule(ctx, h.Pool, id, in.rule(), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_rule_updated", r.ID)
	c.JSON(http.StatusOK, ruleOf(r))
}

func (h *Handler) deleteRule(c *gin.Context) {
	id, ok := web.PathUUID(c, "ruleId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := DeleteRule(ctx, h.Pool, id); err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_rule_deleted", id)
	c.Status(http.StatusNoContent)
}

func (h *Handler) replaceWeeklyHours(c *gin.Context) {
	var in weeklyHoursInput
	if !web.BindJSON(c, &in) {
		return
	}
	rules := make([]Rule, len(in.Periods))
	for i, p := range in.Periods {
		rules[i] = p.rule()
	}
	ctx := c.Request.Context()
	rows, err := ReplaceRules(ctx, h.Pool, rules)
	if err != nil {
		web.Error(c, err)
		return
	}
	tz, _, err := PracticeZone(ctx, db.New(h.Pool))
	if err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "weekly_hours_replaced", "request_id", web.RequestID(ctx), "periods", len(rows))
	c.JSON(http.StatusOK, gin.H{"timezone": tz, "items": rulesOf(rows)})
}

func (h *Handler) listOverrides(c *gin.Context) {
	var in dateRangeQuery
	if !web.BindQuery(c, &in) {
		return
	}
	ctx := c.Request.Context()
	q := db.New(h.Pool)
	tz, loc, err := PracticeZone(ctx, q)
	if err != nil {
		web.Error(c, err)
		return
	}
	first, last, err := DateWindow(h.Now(), loc, dateOf(in.From), dateOf(in.To))
	if err != nil {
		web.Error(c, err)
		return
	}
	rows, err := ListOverrides(ctx, q, first, last)
	if err != nil {
		web.Error(c, err)
		return
	}
	items := make([]overrideJSON, len(rows))
	for i, r := range rows {
		items[i] = overrideOf(r)
	}
	c.JSON(http.StatusOK, gin.H{"timezone": tz, "items": items})
}

func (h *Handler) createOverride(c *gin.Context) {
	var in overrideInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	o, err := CreateOverride(ctx, h.Pool, in.override(), actor(ctx))
	if err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_override_created", o.ID)
	c.JSON(http.StatusCreated, overrideOf(o))
}

func (h *Handler) updateOverride(c *gin.Context) {
	id, ok := web.PathUUID(c, "overrideId")
	if !ok {
		return
	}
	var in overrideInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	o, err := UpdateOverride(ctx, h.Pool, id, in.override(), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_override_updated", o.ID)
	c.JSON(http.StatusOK, overrideOf(o))
}

func (h *Handler) deleteOverride(c *gin.Context) {
	id, ok := web.PathUUID(c, "overrideId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := DeleteOverride(ctx, h.Pool, id); err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_override_deleted", id)
	c.Status(http.StatusNoContent)
}

func (h *Handler) listBlocks(c *gin.Context) {
	var in dateRangeQuery
	if !web.BindQuery(c, &in) {
		return
	}
	ctx := c.Request.Context()
	q := db.New(h.Pool)
	tz, loc, err := PracticeZone(ctx, q)
	if err != nil {
		web.Error(c, err)
		return
	}
	first, last, err := DateWindow(h.Now(), loc, dateOf(in.From), dateOf(in.To))
	if err != nil {
		web.Error(c, err)
		return
	}
	rows, err := ListBlocks(ctx, q, LocalDays(first, last, loc))
	if err != nil {
		web.Error(c, err)
		return
	}
	items := make([]blockJSON, len(rows))
	for i, r := range rows {
		items[i] = blockOf(r)
	}
	c.JSON(http.StatusOK, gin.H{"timezone": tz, "items": items})
}

// createBlock answers the appointments the block overlaps, which it leaves alone.
func (h *Handler) createBlock(c *gin.Context) {
	var in blockInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	saved, err := CreateBlock(ctx, h.Pool, in.block(), actor(ctx))
	if err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_block_created", saved.Block.ID, "conflicts", len(saved.Conflicts))
	c.JSON(http.StatusCreated, savedBlockOf(saved))
}

func (h *Handler) updateBlock(c *gin.Context) {
	id, ok := web.PathUUID(c, "blockId")
	if !ok {
		return
	}
	var in blockInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	saved, err := UpdateBlock(ctx, h.Pool, id, in.block(), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_block_updated", saved.Block.ID, "conflicts", len(saved.Conflicts))
	c.JSON(http.StatusOK, savedBlockOf(saved))
}

func (h *Handler) deleteBlock(c *gin.Context) {
	id, ok := web.PathUUID(c, "blockId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := DeleteBlock(ctx, h.Pool, id); err != nil {
		web.Error(c, err)
		return
	}
	logAvailability(ctx, "availability_block_deleted", id)
	c.Status(http.StatusNoContent)
}

// previewAvailability shows the slots the public sees, even while public booking is paused.
func (h *Handler) previewAvailability(c *gin.Context) {
	var in previewQuery
	if !web.BindQuery(c, &in) {
		return
	}
	id, ok := web.ParseUUID(c, "serviceId", in.ServiceID)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	q := db.New(h.Pool)
	svc, err := GetService(ctx, q, id)
	if err != nil {
		web.Error(c, err)
		return
	}
	a, err := Slots(ctx, q, svc, *dateOf(in.From), *dateOf(in.To), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, previewResponse{
		ServiceID: svc.ID.String(),
		Timezone:  a.Timezone,
		From:      a.First.Format(time.DateOnly),
		To:        a.Last.Format(time.DateOnly),
		Days:      slotDaysJSON(a.Days),
	})
}

// logAvailability logs by id only: notes and reasons are private.
func logAvailability(ctx context.Context, event string, id pgtype.UUID, attrs ...any) {
	slog.InfoContext(ctx, event, append([]any{"request_id", web.RequestID(ctx), "id", id.String()}, attrs...)...)
}

// actor is the signed-in admin; the Admin group has already checked the session.
func actor(ctx context.Context) pgtype.UUID {
	session, _ := auth.FromContext(ctx)
	return session.User.ID
}

func (in ruleInput) rule() Rule {
	return Rule{Weekday: *in.Weekday, Start: in.StartTime, End: in.EndTime}
}

func (in overrideInput) override() Override {
	return Override{OnDate: *dateOf(in.OnDate), Kind: in.Kind, Period: periodOf(in.StartsAt, in.EndsAt), Note: textOf(in.Note)}
}

func (in blockInput) block() Block {
	return Block{Period: periodOf(in.StartsAt, in.EndsAt), AllDay: in.AllDay, Reason: textOf(in.Reason)}
}

// dateOf reads a date binding has checked, as midnight UTC; an absent one is nil.
func dateOf(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, _ := time.Parse(time.DateOnly, s)
	return &t
}

// periodOf reads two times binding has checked.
func periodOf(start, end string) Period {
	s, _ := time.Parse(time.RFC3339, start)
	e, _ := time.Parse(time.RFC3339, end)
	return Period{Start: s, End: e}
}

func textOf(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func stringOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func ruleOf(r db.AvailabilityRule) ruleJSON {
	return ruleJSON{ID: r.ID, Weekday: r.Weekday, StartTime: Clock(r.StartTime), EndTime: Clock(r.EndTime)}
}

func rulesOf(rows []db.AvailabilityRule) []ruleJSON {
	out := make([]ruleJSON, len(rows))
	for i, r := range rows {
		out[i] = ruleOf(r)
	}
	return out
}

func overrideOf(r db.AvailabilityOverride) overrideJSON {
	p := PeriodOf(r.Period)
	return overrideJSON{
		ID:        r.ID,
		OnDate:    r.OnDate.Time.Format(time.DateOnly),
		Kind:      r.Kind,
		StartsAt:  p.Start.UTC(),
		EndsAt:    p.End.UTC(),
		Note:      stringOf(r.Note),
		CreatedAt: r.CreatedAt.UTC(),
	}
}

func blockOf(r db.AvailabilityBlock) blockJSON {
	p := PeriodOf(r.Period)
	return blockJSON{
		ID:        r.ID,
		StartsAt:  p.Start.UTC(),
		EndsAt:    p.End.UTC(),
		AllDay:    r.AllDay,
		Reason:    stringOf(r.Reason),
		CreatedAt: r.CreatedAt.UTC(),
	}
}

func savedBlockOf(saved SavedBlock) savedBlockJSON {
	conflicts := make([]conflictJSON, len(saved.Conflicts))
	for i, a := range saved.Conflicts {
		conflicts[i] = conflictJSON{
			ID:              a.ID,
			Reference:       a.Reference,
			Status:          a.Status,
			Service:         serviceRefJSON{ID: a.ServiceID, Slug: a.ServiceSlug, Name: a.ServiceName},
			StartsAt:        a.StartsAt.UTC(),
			EndsAt:          a.EndsAt.UTC(),
			DurationMinutes: a.DurationMinutes,
			Timezone:        a.Timezone,
			Format:          a.Format,
			Source:          a.Source,
			VisitorName:     a.VisitorName,
			CreatedAt:       a.CreatedAt.UTC(),
			UpdatedAt:       a.UpdatedAt.UTC(),
		}
		if a.HoldExpiresAt.Valid {
			conflicts[i].HoldExpiresAt = new(a.HoldExpiresAt.Time.UTC())
		}
	}
	return savedBlockJSON{Block: blockOf(saved.Block), Conflicts: conflicts}
}
