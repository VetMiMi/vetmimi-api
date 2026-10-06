package comms_test

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

var sydney, _ = time.LoadLocation("Australia/Sydney")

// fixture is an appointment on Monday 5 October 2026, the day after daylight
// saving starts, moved there from a Friday still in standard time.
func fixture(t *testing.T, locale string) comms.RenderData {
	t.Helper()
	var id pgtype.UUID
	require.NoError(t, id.Scan("8f14e45f-ceea-4e7a-9b1f-2c3d4e5f6a7b"))
	start := time.Date(2026, 10, 5, 10, 0, 0, 0, sydney)
	appt := db.GetAppointmentForMessageRow{
		ID: id, Reference: "VM-7KQ2PX", StartsAt: start, EndsAt: start.Add(time.Hour), DurationMinutes: 60,
		Timezone: "Australia/Sydney", Format: "online", Status: "confirmed",
		VisitorName: "Thandar", VisitorEmail: "thandar@example.com",
		VisitorPhone:   pgtype.Text{String: "+61 400 000 000", Valid: true},
		VisitorNote:    pgtype.Text{String: "Mornings suit me best.", Valid: true},
		ServiceName:    []byte(`{"en": "Individual Art Therapy", "my": "တစ်ဦးချင်း အနုပညာကုထုံး"}`),
		ServiceFeeText: []byte(`{"en": "AUD 150 per session", "my": "ဆက်ရှင်တစ်ခုလျှင် AUD 150"}`),
		ServicePreparationText: []byte(`{"en": "Please find a quiet, private space with a few art materials nearby.",
			"my": "အနုပညာ ပစ္စည်း အနည်းငယ်နှင့်အတူ တိတ်ဆိတ်ပြီး သီးသန့်ကျတဲ့ နေရာတစ်ခုမှာ ရှိနေပေးပါ။"}`),
	}
	s := settings.Settings{
		PaymentMethods: []string{"bank_transfer", "card"}, InvoiceTiming: "after_session",
		CancellationNoticeHours: 48, LateCancellationFeePercent: 50, LateCancellationFirstWaived: true,
		NoShowFeePercent: 100, ContactEmail: "hello@vetmimi.example",
		ResponseTime: settings.Localized{En: "Usually within 2 business days"},
	}
	d, err := comms.DataFor(appt, s, "https://vetmimi.example", locale, time.Date(2026, 10, 2, 14, 0, 0, 0, sydney))
	require.NoError(t, err)
	d.ManageURL = "https://vetmimi.example/manage/example-token"
	d.JoinURL = "https://vetmimi.example/session/example-token"
	d.MessageToVisitor = "I am away that week. I would love to see you the week after."
	d.EnquirySubject = "A workshop for our team"
	d.EnquiryMessage = "Hello Daw Mi,\nWe would like to plan a wellbeing workshop in November."
	d.EnquiryOrganisation = "Inner West Community Health"
	d.EnquiryType = "Workshop / Program"
	d.LateCancellation = true
	d.ClientMessage = "Something came up at work, sorry."
	d.PreferredTimes = []string{"Tuesday 6 October 2026, 2:00 pm AEDT", "Thursday 8 October 2026, 10:00 am AEDT"}
	return d
}

// Every kind renders in every locale and matches its reviewed golden file;
// go test ./internal/comms -update rewrites them after a wording change.
func TestRender_Golden(t *testing.T) {
	uuid := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	for _, kind := range comms.Kinds {
		for _, locale := range comms.Locales {
			email, err := comms.Render(kind, locale, fixture(t, locale))
			require.NoError(t, err, "%s.%s", kind, locale)
			for ext, got := range map[string]string{"subject": email.Subject + "\n", "txt": email.Text, "html": email.HTML} {
				path := filepath.Join("testdata", string(kind)+"."+locale+"."+ext)
				if *update {
					require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
				}
				want, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, string(want), got, path)

				require.NotContains(t, got, "admin_note", path)
				if kind.Audience() == comms.Visitor {
					require.NotRegexp(t, uuid, got, "%s shows an id", path)
					require.NotContains(t, strings.ToLower(got), "reason", path)
					require.NotContains(t, got, "Mornings suit me best", "%s shows the visitor's note", path)
				}
			}
		}
	}
}

func TestRender_RequestReceivedSaysPending(t *testing.T) {
	notConfirmed := map[string]string{"en": "not yet confirmed", "my": "အတည်မပြုရသေးပါ"}
	for _, locale := range comms.Locales {
		email, err := comms.Render(comms.RequestReceived, locale, fixture(t, locale))
		require.NoError(t, err)
		for _, body := range []string{email.Text, email.HTML} {
			require.Contains(t, body, "Status: Pending", locale)
			require.Contains(t, body, notConfirmed[locale], locale)
		}
	}
}

func TestRender_RescheduledShowsTheNewTimeFirst(t *testing.T) {
	d := fixture(t, "en")
	email, err := comms.Render(comms.Rescheduled, "en", d)
	require.NoError(t, err)
	for _, body := range []string{email.Text, email.HTML} {
		newAt, oldAt := strings.Index(body, d.Start), strings.Index(body, d.PreviousStart)
		require.True(t, newAt >= 0 && oldAt > newAt, "new time first")
	}
}

func TestRender_EscapesVisitorTextInHTML(t *testing.T) {
	d := fixture(t, "en")
	d.VisitorName = `<script>alert(1)</script>`
	email, err := comms.Render(comms.PractitionerNewRequest, "en", d)
	require.NoError(t, err)
	require.NotContains(t, email.HTML, "<script>")
	require.Contains(t, email.HTML, "&lt;script&gt;")
}

func TestTemplates_AllKindsPresent(t *testing.T) {
	_, err := comms.ParseTemplates(comms.TemplateFiles)
	require.NoError(t, err)

	files := fstest.MapFS{}
	entries, err := comms.TemplateFiles.ReadDir("templates")
	require.NoError(t, err)
	for _, e := range entries {
		body, err := comms.TemplateFiles.ReadFile("templates/" + e.Name())
		require.NoError(t, err)
		files["templates/"+e.Name()] = &fstest.MapFile{Data: body}
	}
	delete(files, "templates/reminder.my.tmpl")
	_, err = comms.ParseTemplates(files)
	require.ErrorContains(t, err, "reminder.my")
}

// Clocks go back at 03:00 AEDT on 5 April 2026; the zone name follows the
// instant, never a fixed offset.
func TestFormatTime_FollowsDaylightSaving(t *testing.T) {
	before := time.Date(2026, 4, 4, 15, 30, 0, 0, time.UTC) // 02:30 AEDT
	after := before.Add(2 * time.Hour)                      // 03:30 AEST
	require.Equal(t, "Sunday 5 April 2026, 2:30 am AEDT", comms.FormatTime(before, sydney, "en"))
	require.Equal(t, "Sunday 5 April 2026, 3:30 am AEST", comms.FormatTime(after, sydney, "en"))
	require.Equal(t, "တနင်္ဂနွေနေ့၊ 2026 ဧပြီလ 5 ရက်၊ နံနက် 3:30 AEST", comms.FormatTime(after, sydney, "my"))
}

// The join block follows the format and the link mode (ADR-007): the room
// link with when it opens, Daw Mi's own link, a promise of details, or
// nothing in person.
func TestRender_JoinBlock(t *testing.T) {
	for name, c := range map[string]struct {
		edit       func(*comms.RenderData)
		has, hasnt []string
	}{
		"room": {func(d *comms.RenderData) {}, []string{"How to join", "/session/example-token", "opens 15 minutes"},
			[]string{"will send"}},
		"manual link": {func(d *comms.RenderData) { d.JoinURL, d.MeetingLink = "", "https://meet.example/abc" },
			[]string{"How to join", "https://meet.example/abc"}, []string{"/session/", "will send"}},
		"no link yet": {func(d *comms.RenderData) { d.JoinURL = "" },
			[]string{"How to join", "Daw Mi will send you the details"}, []string{"/session/"}},
		"in person": {func(d *comms.RenderData) { d.Format = "in_person" }, nil,
			[]string{"How to join", "/session/", "will send"}},
	} {
		for _, kind := range []comms.Kind{comms.BookingConfirmed, comms.Reminder, comms.Rescheduled} {
			d := fixture(t, "en")
			c.edit(&d)
			email, err := comms.Render(kind, "en", d)
			require.NoError(t, err)
			for _, body := range []string{email.Text, email.HTML} {
				for _, s := range c.has {
					require.Contains(t, body, s, "%s %s", name, kind)
				}
				for _, s := range c.hasnt {
					require.NotContains(t, body, s, "%s %s", name, kind)
				}
			}
		}
	}
}
