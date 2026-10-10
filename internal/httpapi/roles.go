package httpapi

import (
	"net/http"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// auth.HasRole lets site_admin act as either other role, so each row names the
// narrowest role allowed.
var (
	everyRole     = []auth.Role{auth.ContentEditor, auth.BookingAdmin, auth.SiteAdmin}
	bookingAdmin  = []auth.Role{auth.BookingAdmin}
	contentEditor = []auth.Role{auth.ContentEditor}
	siteAdmin     = []auth.Role{auth.SiteAdmin}
)

// rolesByOperation is who may call each sessionToken operation. An operation
// missing here allows no one. Finer rules, such as settings key groups, belong
// to the domain.
var rolesByOperation = map[string][]auth.Role{
	"deleteCurrentSession": everyRole,
	"getCurrentUser":       everyRole,
	"getSettings":          everyRole,
	"updateSettings":       bookingAdmin,

	"listServices":  bookingAdmin,
	"createService": bookingAdmin,
	"getService":    bookingAdmin,
	"updateService": bookingAdmin,
	"deleteService": bookingAdmin,
	"pauseService":  bookingAdmin,
	"resumeService": bookingAdmin,

	"listAvailabilityRules":      bookingAdmin,
	"createAvailabilityRule":     bookingAdmin,
	"updateAvailabilityRule":     bookingAdmin,
	"deleteAvailabilityRule":     bookingAdmin,
	"listAvailabilityOverrides":  bookingAdmin,
	"createAvailabilityOverride": bookingAdmin,
	"updateAvailabilityOverride": bookingAdmin,
	"deleteAvailabilityOverride": bookingAdmin,
	"listAvailabilityBlocks":     bookingAdmin,
	"createAvailabilityBlock":    bookingAdmin,
	"updateAvailabilityBlock":    bookingAdmin,
	"deleteAvailabilityBlock":    bookingAdmin,
	"previewAvailability":        bookingAdmin,

	"getBookingDashboard":         bookingAdmin,
	"listAppointments":            bookingAdmin,
	"createManualAppointment":     bookingAdmin,
	"getAppointment":              bookingAdmin,
	"confirmAppointment":          bookingAdmin,
	"declineAppointment":          bookingAdmin,
	"rescheduleAppointment":       bookingAdmin,
	"cancelAppointment":           bookingAdmin,
	"completeAppointment":         bookingAdmin,
	"markAppointmentNoShow":       bookingAdmin,
	"setAppointmentNote":          bookingAdmin,
	"setAppointmentMeetingLink":   bookingAdmin,
	"resendCommunication":         bookingAdmin,
	"markAppointmentCommunicated": bookingAdmin,
	"listCommunications":          bookingAdmin,
	"startVideoSession":           bookingAdmin,
	"endVideoSession":             bookingAdmin,
	"listContactEnquiries":        bookingAdmin,
	"getContactEnquiry":           bookingAdmin,
	"markContactEnquiryHandled":   bookingAdmin,

	// Editors write and submit; only a site administrator reviews and publishes.
	"listPosts":             contentEditor,
	"createPost":            contentEditor,
	"getPost":               contentEditor,
	"updatePost":            contentEditor,
	"deletePost":            contentEditor,
	"submitPost":            contentEditor,
	"markPostChannelPosted": contentEditor,
	"retryPostChannel":      contentEditor,
	"requestPostChanges":    siteAdmin,
	"approvePost":           siteAdmin,
	"schedulePost":          siteAdmin,
	"unschedulePost":        siteAdmin,
	"publishPost":           siteAdmin,
	"archivePost":           siteAdmin,

	"listMedia":   contentEditor,
	"uploadMedia": contentEditor,
	"getMedia":    contentEditor,
	"updateMedia": contentEditor,
	"deleteMedia": contentEditor,

	"suggestPostVersions": contentEditor,
	"getAIStatus":         contentEditor,

	"getMetaConnection":    siteAdmin,
	"startMetaConnection":  siteAdmin,
	"finishMetaConnection": siteAdmin,
	"chooseMetaPage":       siteAdmin,
	"disconnectMeta":       siteAdmin,

	"getLinkedInConnection":    siteAdmin,
	"startLinkedInConnection":  siteAdmin,
	"finishLinkedInConnection": siteAdmin,
	"disconnectLinkedIn":       siteAdmin,
}

// requireRoles answers 403 to a sessionToken operation the user's roles do
// not allow. The body is the same for every operation, so it names nothing.
func requireRoles(ops operations) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, _ := ops.lookup(r)
			if op.Security != schemeSessionToken {
				next.ServeHTTP(w, r)
				return
			}
			session, _ := auth.FromContext(r.Context())
			if !permits(rolesByOperation[op.ID], session.User.Roles) {
				writeProblem(w, apperr.New(apperr.Forbidden, "Your role does not allow this."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func permits(allowed []auth.Role, held []string) bool {
	for _, r := range allowed {
		if auth.HasRole(held, r) {
			return true
		}
	}
	return false
}
