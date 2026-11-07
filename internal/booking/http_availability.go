package booking

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type previewAvailabilityQuery struct {
	ServiceID string `form:"serviceId" binding:"required"`
	From      string `form:"from" binding:"required,datetime=2006-01-02"`
	To        string `form:"to" binding:"required,datetime=2006-01-02"`
}

type ruleBody struct {
	Weekday   *int16 `json:"weekday" binding:"required,min=1,max=7"`
	StartTime string `json:"startTime" binding:"required,hhmm"`
	EndTime   string `json:"endTime" binding:"required,hhmm"`
}

type replaceWeeklyHoursBody struct {
	Periods []ruleBody `json:"periods" binding:"required,max=70,dive"`
}

type ruleListResponse struct {
	Timezone string         `json:"timezone"`
	Items    []ruleResponse `json:"items"`
}

type ruleResponse struct {
	ID        pgtype.UUID `json:"id"`
	Weekday   int16       `json:"weekday"`
	StartTime string      `json:"startTime"`
	EndTime   string      `json:"endTime"`
}

type previewResponse struct {
	ServiceID string            `json:"serviceId"`
	Timezone  string            `json:"timezone"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Days      []slotDayResponse `json:"days"`
}

func (h *Handler) listRules(c *gin.Context) {
	ctx := c.Request.Context()

	q := db.New(h.Pool)
	timezone, _, err := PracticeZone(ctx, q)
	if err != nil {
		web.Error(c, err)
		return
	}
	rules, err := ListRules(ctx, q)
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newRuleListResponse(timezone, rules))
}

func (h *Handler) createRule(c *gin.Context) {
	var body ruleBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	rule, err := CreateRule(ctx, h.Pool, newRule(body))
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_rule_created", rule.ID)
	c.JSON(http.StatusCreated, newRuleResponse(rule))
}

func (h *Handler) replaceWeeklyHours(c *gin.Context) {
	var body replaceWeeklyHoursBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	rules := make([]Rule, len(body.Periods))
	for i, period := range body.Periods {
		rules[i] = newRule(period)
	}
	saved, err := ReplaceRules(ctx, h.Pool, rules)
	if err != nil {
		web.Error(c, err)
		return
	}
	timezone, _, err := PracticeZone(ctx, db.New(h.Pool))
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "weekly_hours_replaced", "request_id", web.RequestID(ctx), "periods", len(saved))
	c.JSON(http.StatusOK, newRuleListResponse(timezone, saved))
}

func (h *Handler) updateRule(c *gin.Context) {
	ruleID, ok := web.PathUUID(c, "ruleId")
	if !ok {
		return
	}
	var body ruleBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	rule, err := UpdateRule(ctx, h.Pool, ruleID, newRule(body), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_rule_updated", rule.ID)
	c.JSON(http.StatusOK, newRuleResponse(rule))
}

func (h *Handler) deleteRule(c *gin.Context) {
	ruleID, ok := web.PathUUID(c, "ruleId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	err := DeleteRule(ctx, h.Pool, ruleID)
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_rule_deleted", ruleID)
	c.Status(http.StatusNoContent)
}

// previewAvailability shows the slots the public sees, even while public booking is paused.
func (h *Handler) previewAvailability(c *gin.Context) {
	var query previewAvailabilityQuery
	if !web.BindQuery(c, &query) {
		return
	}
	serviceID, ok := web.ParseUUID(c, "serviceId", query.ServiceID)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	q := db.New(h.Pool)
	service, err := GetService(ctx, q, serviceID)
	if err != nil {
		web.Error(c, err)
		return
	}
	availability, err := Slots(ctx, q, service, *dateOf(query.From), *dateOf(query.To), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	response := previewResponse{
		ServiceID: service.ID.String(),
		Timezone:  availability.Timezone,
		From:      availability.First.Format(time.DateOnly),
		To:        availability.Last.Format(time.DateOnly),
		Days:      newSlotDayResponses(availability.Days),
	}
	c.JSON(http.StatusOK, response)
}

func newRuleListResponse(timezone string, rules []db.AvailabilityRule) ruleListResponse {
	response := ruleListResponse{
		Timezone: timezone,
		Items:    make([]ruleResponse, len(rules)),
	}
	for i, rule := range rules {
		response.Items[i] = newRuleResponse(rule)
	}
	return response
}

func newRuleResponse(rule db.AvailabilityRule) ruleResponse {
	return ruleResponse{
		ID:        rule.ID,
		Weekday:   rule.Weekday,
		StartTime: Clock(rule.StartTime),
		EndTime:   Clock(rule.EndTime),
	}
}

func newRule(body ruleBody) Rule {
	return Rule{
		Weekday: *body.Weekday,
		Start:   body.StartTime,
		End:     body.EndTime,
	}
}

// logAvailability logs by id only: notes and reasons are private.
func logAvailability(ctx context.Context, event string, id pgtype.UUID, attrs ...any) {
	attrs = append([]any{"request_id", web.RequestID(ctx), "id", id.String()}, attrs...)
	slog.InfoContext(ctx, event, attrs...)
}

// dateOf reads a date binding has checked, so parsing cannot fail, as midnight UTC; an absent one is nil.
func dateOf(s string) *time.Time {
	if s == "" {
		return nil
	}
	date, _ := time.Parse(time.DateOnly, s)
	return &date
}

// periodOf reads two times binding has checked, so parsing cannot fail.
func periodOf(start, end string) Period {
	startsAt, _ := time.Parse(time.RFC3339, start)
	endsAt, _ := time.Parse(time.RFC3339, end)
	return Period{Start: startsAt, End: endsAt}
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
