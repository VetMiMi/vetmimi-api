package comms

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// RenderData is a struct, not a map, so templates see only chosen fields.
type RenderData struct {
	Reference       string
	ServiceName     string
	Start, End      string
	PreviousStart   string
	DurationMinutes int
	Format          string
	Status          string
	FeeText         string
	PreparationText string
	PaymentMethods  []string
	InvoiceTiming   string
	Policy          Policy
	ContactEmail    string
	ResponseTime    string
	BookURL         string

	ManageURL              string
	JoinURL                string
	MeetingLink            string
	JoinOpensMinutesBefore int
	MessageToVisitor       string
	LateCancellation       bool

	// Daw Mi's emails only.
	VisitorName    string
	VisitorEmail   string
	VisitorPhone   string
	VisitorNote    string
	AdminURL       string
	ClientMessage  string
	PreferredTimes []string

	EnquirySubject      string
	EnquiryMessage      string
	EnquiryOrganisation string
	EnquiryType         string
}

type Policy struct {
	NoticeHours      int
	LateFeePercent   int
	FirstWaived      bool
	NoShowFeePercent int
}

var (
	myWeekdays = [...]string{"တနင်္ဂနွေနေ့", "တနင်္လာနေ့", "အင်္ဂါနေ့", "ဗုဒ္ဓဟူးနေ့", "ကြာသပတေးနေ့", "သောကြာနေ့", "စနေနေ့"}
	myMonths   = [...]string{"ဇန်နဝါရီလ", "ဖေဖော်ဝါရီလ", "မတ်လ", "ဧပြီလ", "မေလ", "ဇွန်လ",
		"ဇူလိုင်လ", "ဩဂုတ်လ", "စက်တင်ဘာလ", "အောက်တိုဘာလ", "နိုဝင်ဘာလ", "ဒီဇင်ဘာလ"}
)

// DataFor's previous is the start before a reschedule, or zero.
func DataFor(appointment db.GetAppointmentForMessageRow, current settings.Settings, siteURL, locale string,
	previous time.Time) (RenderData, error) {
	location, err := time.LoadLocation(appointment.Timezone)
	if err != nil {
		return RenderData{}, err
	}
	data := RenderData{
		Reference:       appointment.Reference,
		ServiceName:     localized(appointment.ServiceName, locale),
		Start:           FormatTime(appointment.StartsAt, location, locale),
		End:             FormatTime(appointment.EndsAt, location, locale),
		DurationMinutes: int(appointment.DurationMinutes),
		Format:          appointment.Format,
		Status:          appointment.Status,
		FeeText:         localized(appointment.ServiceFeeText, locale),
		PreparationText: localized(appointment.ServicePreparationText, locale),
		PaymentMethods:  current.PaymentMethods,
		InvoiceTiming:   current.InvoiceTiming,
		Policy: Policy{
			NoticeHours:      current.CancellationNoticeHours,
			LateFeePercent:   current.LateCancellationFeePercent,
			FirstWaived:      current.LateCancellationFirstWaived,
			NoShowFeePercent: current.NoShowFeePercent,
		},
		ContactEmail: current.ContactEmail,
		ResponseTime: pick(current.ResponseTime, locale),
		BookURL:      sitePath(siteURL, locale, "/book"),
		MeetingLink:  appointment.MeetingLink.String,

		JoinOpensMinutesBefore: int(video.OpensBefore / time.Minute),
		VisitorName:            appointment.VisitorName,
		VisitorEmail:           appointment.VisitorEmail,
		VisitorPhone:           appointment.VisitorPhone.String,
		VisitorNote:            appointment.VisitorNote.String,
		AdminURL:               strings.TrimRight(siteURL, "/") + "/admin/appointments/" + appointment.ID.String(),

		LateCancellation: appointment.LateCancellation,
	}
	if !previous.IsZero() {
		data.PreviousStart = FormatTime(previous, location, locale)
	}
	return data, nil
}

// FormatTime matches the site's booking pages, ending in AEST or AEDT.
func FormatTime(at time.Time, location *time.Location, locale string) string {
	at = at.In(location)
	if locale != "my" {
		return at.Format("Monday 2 January 2006, 3:04 pm MST")
	}
	period := "နံနက်"
	if at.Hour() >= 12 {
		period = "မွန်းလွဲ"
	}
	return fmt.Sprintf("%s၊ %d %s %d ရက်၊ %s %s", myWeekdays[at.Weekday()], at.Year(), myMonths[at.Month()-1],
		at.Day(), period, at.Format("3:04 MST"))
}

func sitePath(siteURL, locale, path string) string {
	base := strings.TrimRight(siteURL, "/")
	if locale != "en" {
		base += "/" + locale
	}
	return base + path
}

func localized(raw json.RawMessage, locale string) string {
	var text settings.Localized
	if len(raw) == 0 || json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return pick(text, locale)
}

func pick(text settings.Localized, locale string) string {
	if locale == "my" && text.My != "" {
		return text.My
	}
	return text.En
}
