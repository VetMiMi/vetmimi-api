package httpapi

import (
	"net/http"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// The role sets the table below uses. auth.HasRole lets site_admin act as
// either of the other two, so a row names the narrowest role that may use
// it, and site_admin alone only where the other two may not.
var (
	everyRole     = []auth.Role{auth.ContentEditor, auth.BookingAdmin, auth.SiteAdmin}
	bookingAdmin  = []auth.Role{auth.BookingAdmin}
	contentEditor = []auth.Role{auth.ContentEditor}
	siteAdmin     = []auth.Role{auth.SiteAdmin}
)

// rolesByOperation is the roles each sessionToken operation in openapi.yaml
// allows: docs/architecture.md, "Authentication and roles". Content editors
// never reach booking data and booking administrators never edit content
// (ADR-002). TestEverySessionOperationHasRoles keeps it in step with the
// contract. Rules finer than an operation, such as settings key groups or
// Facebook statuses past permission_pending, belong to the domain, through
// auth.FromContext.
var rolesByOperation = map[string][]auth.Role{
	// The settings domain decides which keys each role sees and changes.
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

	// Each kind of content: editors draft and submit; only a site
	// administrator approves, publishes or takes it down.
	"listStories":           contentEditor,
	"createStory":           contentEditor,
	"getStory":              contentEditor,
	"updateStory":           contentEditor,
	"submitStoryForReview":  contentEditor,
	"listStoryVersions":     contentEditor,
	"restoreStoryVersion":   contentEditor,
	"listStoryRelations":    contentEditor,
	"replaceStoryRelations": contentEditor,
	"approveStory":          siteAdmin,
	"requestChangesToStory": siteAdmin,
	"publishStory":          siteAdmin,
	"scheduleStory":         siteAdmin,
	"unpublishStory":        siteAdmin,
	"archiveStory":          siteAdmin,

	"listServicePages":            contentEditor,
	"createServicePage":           contentEditor,
	"getServicePage":              contentEditor,
	"updateServicePage":           contentEditor,
	"submitServicePageForReview":  contentEditor,
	"listServicePageVersions":     contentEditor,
	"restoreServicePageVersion":   contentEditor,
	"listServicePageRelations":    contentEditor,
	"replaceServicePageRelations": contentEditor,
	"approveServicePage":          siteAdmin,
	"requestChangesToServicePage": siteAdmin,
	"publishServicePage":          siteAdmin,
	"scheduleServicePage":         siteAdmin,
	"unpublishServicePage":        siteAdmin,
	"archiveServicePage":          siteAdmin,

	"listPortfolioItems":            contentEditor,
	"createPortfolioItem":           contentEditor,
	"getPortfolioItem":              contentEditor,
	"updatePortfolioItem":           contentEditor,
	"submitPortfolioItemForReview":  contentEditor,
	"listPortfolioItemVersions":     contentEditor,
	"restorePortfolioItemVersion":   contentEditor,
	"listPortfolioItemRelations":    contentEditor,
	"replacePortfolioItemRelations": contentEditor,
	"approvePortfolioItem":          siteAdmin,
	"requestChangesToPortfolioItem": siteAdmin,
	"publishPortfolioItem":          siteAdmin,
	"schedulePortfolioItem":         siteAdmin,
	"unpublishPortfolioItem":        siteAdmin,
	"archivePortfolioItem":          siteAdmin,

	"listPages":            contentEditor,
	"createPage":           contentEditor,
	"getPage":              contentEditor,
	"updatePage":           contentEditor,
	"submitPageForReview":  contentEditor,
	"listPageVersions":     contentEditor,
	"restorePageVersion":   contentEditor,
	"listPageRelations":    contentEditor,
	"replacePageRelations": contentEditor,
	"approvePage":          siteAdmin,
	"requestChangesToPage": siteAdmin,
	"publishPage":          siteAdmin,
	"schedulePage":         siteAdmin,
	"unpublishPage":        siteAdmin,
	"archivePage":          siteAdmin,
	"createPageSection":    contentEditor,
	"reorderPageSections":  contentEditor,
	"updatePageSection":    contentEditor,
	"deletePageSection":    contentEditor,

	"getFeaturedArea":     siteAdmin,
	"replaceFeaturedArea": siteAdmin,

	"listMedia":    contentEditor,
	"uploadMedia":  contentEditor,
	"getMedia":     contentEditor,
	"updateMedia":  contentEditor,
	"archiveMedia": contentEditor,
	"deleteMedia":  siteAdmin,

	// The content domain keeps statuses past permission_pending for site
	// administrators.
	"listFacebookPosts":     contentEditor,
	"createFacebookPost":    contentEditor,
	"getFacebookPost":       contentEditor,
	"updateFacebookPost":    contentEditor,
	"deleteFacebookPost":    contentEditor,
	"setFacebookPostStatus": contentEditor,
}

// requireRoles answers 403 forbidden to a request for a sessionToken
// operation whose user holds none of the roles rolesByOperation allows it.
// The body is the same for every operation, so a refusal names no route or
// resource (Booking & Admin UX, section 29). An operation missing from the
// table allows no one: a new signed-in route stays closed until it is given
// roles. It runs after the session check, which put the session in the
// context; other operations are left alone.
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

// permits reports whether a user holding held may act as any of allowed.
func permits(allowed []auth.Role, held []string) bool {
	for _, r := range allowed {
		if auth.HasRole(held, r) {
			return true
		}
	}
	return false
}
