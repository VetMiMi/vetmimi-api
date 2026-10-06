package httpapi

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// ListAvailabilityRules lists the weekly periods with the timezone they are
// read in.
func (s *server) ListAvailabilityRules(ctx context.Context, _ gen.ListAvailabilityRulesRequestObject) (gen.ListAvailabilityRulesResponseObject, error) {
	q := db.New(s.Pool)
	tz, _, err := booking.PracticeZone(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := booking.ListRules(ctx, q)
	if err != nil {
		return nil, err
	}
	items := make([]gen.AvailabilityRule, len(rows))
	for i, r := range rows {
		items[i] = ruleView(r)
	}
	return gen.ListAvailabilityRules200JSONResponse{Timezone: tz, Items: items}, nil
}

// CreateAvailabilityRule saves a weekly period.
func (s *server) CreateAvailabilityRule(ctx context.Context, req gen.CreateAvailabilityRuleRequestObject) (gen.CreateAvailabilityRuleResponseObject, error) {
	r, err := booking.CreateRule(ctx, s.Pool, rule(*req.Body))
	if err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_rule_created", r.ID)
	return gen.CreateAvailabilityRule201JSONResponse(ruleView(r)), nil
}

// UpdateAvailabilityRule replaces a weekly period.
func (s *server) UpdateAvailabilityRule(ctx context.Context, req gen.UpdateAvailabilityRuleRequestObject) (gen.UpdateAvailabilityRuleResponseObject, error) {
	r, err := booking.UpdateRule(ctx, s.Pool, uuid(req.RuleId), rule(*req.Body), s.Now())
	if err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_rule_updated", r.ID)
	return gen.UpdateAvailabilityRule200JSONResponse(ruleView(r)), nil
}

// DeleteAvailabilityRule deletes a weekly period.
func (s *server) DeleteAvailabilityRule(ctx context.Context, req gen.DeleteAvailabilityRuleRequestObject) (gen.DeleteAvailabilityRuleResponseObject, error) {
	if err := booking.DeleteRule(ctx, s.Pool, uuid(req.RuleId)); err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_rule_deleted", uuid(req.RuleId))
	return gen.DeleteAvailabilityRule204Response{}, nil
}

// ListAvailabilityOverrides lists the overrides on the local dates asked
// for, by default the next 62 days.
func (s *server) ListAvailabilityOverrides(ctx context.Context, req gen.ListAvailabilityOverridesRequestObject) (gen.ListAvailabilityOverridesResponseObject, error) {
	q := db.New(s.Pool)
	tz, loc, err := booking.PracticeZone(ctx, q)
	if err != nil {
		return nil, err
	}
	first, last, err := booking.DateWindow(s.Now(), loc, dateOf(req.Params.From), dateOf(req.Params.To))
	if err != nil {
		return nil, err
	}
	rows, err := booking.ListOverrides(ctx, q, first, last)
	if err != nil {
		return nil, err
	}
	items := make([]gen.AvailabilityOverride, len(rows))
	for i, r := range rows {
		items[i] = overrideView(r)
	}
	return gen.ListAvailabilityOverrides200JSONResponse{Timezone: tz, Items: items}, nil
}

// CreateAvailabilityOverride saves a one-off opening or date change.
func (s *server) CreateAvailabilityOverride(ctx context.Context, req gen.CreateAvailabilityOverrideRequestObject) (gen.CreateAvailabilityOverrideResponseObject, error) {
	session, _ := auth.FromContext(ctx)
	o, err := booking.CreateOverride(ctx, s.Pool, override(*req.Body), session.User.ID)
	if err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_override_created", o.ID)
	return gen.CreateAvailabilityOverride201JSONResponse(overrideView(o)), nil
}

// UpdateAvailabilityOverride replaces an override.
func (s *server) UpdateAvailabilityOverride(ctx context.Context, req gen.UpdateAvailabilityOverrideRequestObject) (gen.UpdateAvailabilityOverrideResponseObject, error) {
	o, err := booking.UpdateOverride(ctx, s.Pool, uuid(req.OverrideId), override(*req.Body), s.Now())
	if err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_override_updated", o.ID)
	return gen.UpdateAvailabilityOverride200JSONResponse(overrideView(o)), nil
}

// DeleteAvailabilityOverride deletes an override.
func (s *server) DeleteAvailabilityOverride(ctx context.Context, req gen.DeleteAvailabilityOverrideRequestObject) (gen.DeleteAvailabilityOverrideResponseObject, error) {
	if err := booking.DeleteOverride(ctx, s.Pool, uuid(req.OverrideId)); err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_override_deleted", uuid(req.OverrideId))
	return gen.DeleteAvailabilityOverride204Response{}, nil
}

// ListAvailabilityBlocks lists the blocks touching the local dates asked
// for, by default the next 62 days.
func (s *server) ListAvailabilityBlocks(ctx context.Context, req gen.ListAvailabilityBlocksRequestObject) (gen.ListAvailabilityBlocksResponseObject, error) {
	q := db.New(s.Pool)
	tz, loc, err := booking.PracticeZone(ctx, q)
	if err != nil {
		return nil, err
	}
	first, last, err := booking.DateWindow(s.Now(), loc, dateOf(req.Params.From), dateOf(req.Params.To))
	if err != nil {
		return nil, err
	}
	rows, err := booking.ListBlocks(ctx, q, booking.LocalDays(first, last, loc))
	if err != nil {
		return nil, err
	}
	items := make([]gen.AvailabilityBlock, len(rows))
	for i, r := range rows {
		items[i] = blockView(r)
	}
	return gen.ListAvailabilityBlocks200JSONResponse{Timezone: tz, Items: items}, nil
}

// CreateAvailabilityBlock saves blocked time and lists the appointments it
// overlaps, which it leaves alone.
func (s *server) CreateAvailabilityBlock(ctx context.Context, req gen.CreateAvailabilityBlockRequestObject) (gen.CreateAvailabilityBlockResponseObject, error) {
	session, _ := auth.FromContext(ctx)
	saved, err := booking.CreateBlock(ctx, s.Pool, block(*req.Body), session.User.ID)
	if err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_block_created", saved.Block.ID, "conflicts", len(saved.Conflicts))
	return gen.CreateAvailabilityBlock201JSONResponse(savedBlockView(saved)), nil
}

// UpdateAvailabilityBlock replaces blocked time and lists the appointments
// it overlaps.
func (s *server) UpdateAvailabilityBlock(ctx context.Context, req gen.UpdateAvailabilityBlockRequestObject) (gen.UpdateAvailabilityBlockResponseObject, error) {
	saved, err := booking.UpdateBlock(ctx, s.Pool, uuid(req.BlockId), block(*req.Body), s.Now())
	if err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_block_updated", saved.Block.ID, "conflicts", len(saved.Conflicts))
	return gen.UpdateAvailabilityBlock200JSONResponse(savedBlockView(saved)), nil
}

// DeleteAvailabilityBlock deletes blocked time.
func (s *server) DeleteAvailabilityBlock(ctx context.Context, req gen.DeleteAvailabilityBlockRequestObject) (gen.DeleteAvailabilityBlockResponseObject, error) {
	if err := booking.DeleteBlock(ctx, s.Pool, uuid(req.BlockId)); err != nil {
		return nil, err
	}
	s.logAvailability(ctx, "availability_block_deleted", uuid(req.BlockId))
	return gen.DeleteAvailabilityBlock204Response{}, nil
}

// logAvailability records a change by id only: notes and reasons are private.
func (s *server) logAvailability(ctx context.Context, msg string, id pgtype.UUID, attrs ...any) {
	s.Log.InfoContext(ctx, msg, append([]any{"request_id", RequestID(ctx), "id", id.String()}, attrs...)...)
}

func rule(in gen.AvailabilityRuleInput) booking.Rule {
	return booking.Rule{Weekday: int16(in.Weekday), Start: in.StartTime, End: in.EndTime}
}

func ruleView(r db.AvailabilityRule) gen.AvailabilityRule {
	return gen.AvailabilityRule{
		Id:        openapi_types.UUID(r.ID.Bytes),
		Weekday:   int(r.Weekday),
		StartTime: booking.Clock(r.StartTime),
		EndTime:   booking.Clock(r.EndTime),
	}
}

func override(in gen.AvailabilityOverrideInput) booking.Override {
	return booking.Override{
		OnDate: in.OnDate.Time,
		Kind:   string(in.Kind),
		Period: booking.Period{Start: in.StartsAt, End: in.EndsAt},
		Note:   optionalText(in.Note),
	}
}

func overrideView(r db.AvailabilityOverride) gen.AvailabilityOverride {
	p := booking.PeriodOf(r.Period)
	return gen.AvailabilityOverride{
		Id:        openapi_types.UUID(r.ID.Bytes),
		OnDate:    openapi_types.Date{Time: r.OnDate.Time},
		Kind:      gen.AvailabilityOverrideKind(r.Kind),
		StartsAt:  p.Start.UTC(),
		EndsAt:    p.End.UTC(),
		Note:      textView(r.Note),
		CreatedAt: r.CreatedAt.UTC(),
	}
}

func block(in gen.AvailabilityBlockInput) booking.Block {
	return booking.Block{
		Period: booking.Period{Start: in.StartsAt, End: in.EndsAt},
		AllDay: deref(in.AllDay),
		Reason: optionalText(in.Reason),
	}
}

func blockView(r db.AvailabilityBlock) gen.AvailabilityBlock {
	p := booking.PeriodOf(r.Period)
	return gen.AvailabilityBlock{
		Id:        openapi_types.UUID(r.ID.Bytes),
		StartsAt:  p.Start.UTC(),
		EndsAt:    p.End.UTC(),
		AllDay:    new(r.AllDay),
		Reason:    textView(r.Reason),
		CreatedAt: r.CreatedAt.UTC(),
	}
}

func savedBlockView(saved booking.SavedBlock) gen.AvailabilityBlockSaved {
	conflicts := make([]gen.AppointmentSummary, len(saved.Conflicts))
	for i, a := range saved.Conflicts {
		conflicts[i] = gen.AppointmentSummary{
			Id:        openapi_types.UUID(a.ID.Bytes),
			Reference: a.Reference,
			Status:    gen.AppointmentStatus(a.Status),
			Service: gen.ServiceRef{
				Id:   openapi_types.UUID(a.ServiceID.Bytes),
				Slug: a.ServiceSlug,
				Name: deref(localizedView(a.ServiceName)),
			},
			StartsAt:        a.StartsAt.UTC(),
			EndsAt:          a.EndsAt.UTC(),
			DurationMinutes: int(a.DurationMinutes),
			Timezone:        a.Timezone,
			Format:          gen.Format(a.Format),
			Source:          gen.AppointmentSummarySource(a.Source),
			VisitorName:     a.VisitorName,
			CreatedAt:       a.CreatedAt.UTC(),
			UpdatedAt:       a.UpdatedAt.UTC(),
		}
		if a.HoldExpiresAt.Valid {
			conflicts[i].HoldExpiresAt = new(a.HoldExpiresAt.Time.UTC())
		}
	}
	return gen.AvailabilityBlockSaved{Block: blockView(saved.Block), Conflicts: conflicts}
}

func dateOf(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	return &d.Time
}

func optionalText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func textView(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}
