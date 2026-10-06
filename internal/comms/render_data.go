package comms

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// RenderData is everything a template may show. It is a struct, not a map,
// so a template cannot reach a field nobody chose to give it: admin notes,
// reasons and ids have no field here, except AdminURL, which only the
// practitioner kinds use.
type RenderData struct {
	Reference       string
	ServiceName     string
	Start, End      string // in the practice timezone, with AEST or AEDT
	PreviousStart   string // rescheduled only
	DurationMinutes int
	Format          string // online or in_person
	Status          string // the appointment's status, for Daw Mi
	FeeText         string
	PreparationText string
	PaymentMethods  []string // bank_transfer, card
	InvoiceTiming   string   // after_session
	Policy          Policy
	ContactEmail    string
	ResponseTime    string
	BookURL         string

	// ManageURL is the visitor's management link and JoinURL the VetMiMi room
	// link, which the video issue fills. MeetingLink is the link Daw Mi set in
	// manual_link mode.
	ManageURL   string
	JoinURL     string
	MeetingLink string
	// MessageToVisitor is Daw Mi's own words on a decline or cancellation.
	MessageToVisitor string
	// LateCancellation is set when the visitor cancelled inside the notice
	// period.
	LateCancellation bool

	// For Daw Mi only.
	VisitorName  string
	VisitorEmail string
	VisitorPhone string
	VisitorNote  string
	AdminURL     string
	// ClientMessage is the visitor's own words with a cancellation or a
	// reschedule request; PreferredTimes the starts they offered, formatted.
	ClientMessage  string
	PreferredTimes []string
	// practitioner_new_enquiry only.
	EnquirySubject      string
	EnquiryMessage      string
	EnquiryOrganisation string
	EnquiryType         string
}

// Policy is the late cancellation and no-show terms from settings.
type Policy struct {
	NoticeHours      int
	LateFeePercent   int
	FirstWaived      bool
	NoShowFeePercent int
}

// DataFor assembles what the templates show about appt in locale. previous
// is where a reschedule moved it from, or zero.
func DataFor(appt db.GetAppointmentForMessageRow, s settings.Settings, siteURL, locale string, previous time.Time) (RenderData, error) {
	loc, err := time.LoadLocation(appt.Timezone)
	if err != nil {
		return RenderData{}, err
	}
	d := RenderData{
		Reference:       appt.Reference,
		ServiceName:     localized(appt.ServiceName, locale),
		Start:           FormatTime(appt.StartsAt, loc, locale),
		End:             FormatTime(appt.EndsAt, loc, locale),
		DurationMinutes: int(appt.DurationMinutes),
		Format:          appt.Format,
		Status:          appt.Status,
		FeeText:         localized(appt.ServiceFeeText, locale),
		PreparationText: localized(appt.ServicePreparationText, locale),
		PaymentMethods:  s.PaymentMethods,
		InvoiceTiming:   s.InvoiceTiming,
		Policy: Policy{
			NoticeHours:      s.CancellationNoticeHours,
			LateFeePercent:   s.LateCancellationFeePercent,
			FirstWaived:      s.LateCancellationFirstWaived,
			NoShowFeePercent: s.NoShowFeePercent,
		},
		ContactEmail: s.ContactEmail,
		ResponseTime: pick(s.ResponseTime, locale),
		BookURL:      sitePath(siteURL, locale, "/book"),
		MeetingLink:  appt.MeetingLink.String,
		VisitorName:  appt.VisitorName,
		VisitorEmail: appt.VisitorEmail,
		VisitorPhone: appt.VisitorPhone.String,
		VisitorNote:  appt.VisitorNote.String,
		AdminURL:     strings.TrimRight(siteURL, "/") + "/admin/appointments/" + appt.ID.String(),

		LateCancellation: appt.LateCancellation,
	}
	if !previous.IsZero() {
		d.PreviousStart = FormatTime(previous, loc, locale)
	}
	return d, nil
}

// sitePath is a page of the public site in locale; English has no prefix.
func sitePath(siteURL, locale, path string) string {
	base := strings.TrimRight(siteURL, "/")
	if locale != "en" {
		base += "/" + locale
	}
	return base + path
}

func localized(raw json.RawMessage, locale string) string {
	var l settings.Localized
	if len(raw) == 0 || json.Unmarshal(raw, &l) != nil {
		return ""
	}
	return pick(l, locale)
}

func pick(l settings.Localized, locale string) string {
	if locale == "my" && l.My != "" {
		return l.My
	}
	return l.En
}

var (
	myWeekdays = [...]string{"တနင်္ဂနွေနေ့", "တနင်္လာနေ့", "အင်္ဂါနေ့", "ဗုဒ္ဓဟူးနေ့", "ကြာသပတေးနေ့", "သောကြာနေ့", "စနေနေ့"}
	myMonths   = [...]string{"ဇန်နဝါရီလ", "ဖေဖော်ဝါရီလ", "မတ်လ", "ဧပြီလ", "မေလ", "ဇွန်လ",
		"ဇူလိုင်လ", "ဩဂုတ်လ", "စက်တင်ဘာလ", "အောက်တိုဘာလ", "နိုဝင်ဘာလ", "ဒီဇင်ဘာလ"}
)

// FormatTime writes t as the site's booking pages do (vetmimi-next
// messages/*/book.json, "calendar"), in loc, ending with the zone
// abbreviation the date has there, AEST or AEDT. Burmese keeps Western digits.
func FormatTime(t time.Time, loc *time.Location, locale string) string {
	t = t.In(loc)
	if locale != "my" {
		return t.Format("Monday 2 January 2006, 3:04 pm MST")
	}
	period := "နံနက်"
	if t.Hour() >= 12 {
		period = "မွန်းလွဲ"
	}
	return fmt.Sprintf("%s၊ %d %s %d ရက်၊ %s %s", myWeekdays[t.Weekday()], t.Year(), myMonths[t.Month()-1],
		t.Day(), period, t.Format("3:04 MST"))
}
