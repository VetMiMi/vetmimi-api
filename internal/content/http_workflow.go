package content

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type versionInput struct {
	Version int32 `json:"version" binding:"required,min=1"`
}

func (h *Handler) submitPost(c *gin.Context) {
	var u postURI
	var in versionInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	p, err := Submit(c.Request.Context(), h.Pool, u.id(), in.Version, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	sendPost(c, http.StatusOK, "post_submitted", p)
}

type requestChangesInput struct {
	Version int32  `json:"version" binding:"required,min=1"`
	Note    string `json:"note" binding:"required,max=2000"`
}

func (h *Handler) requestChanges(c *gin.Context) {
	var u postURI
	var in requestChangesInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	p, err := RequestChanges(c.Request.Context(), h.Pool, u.id(), in.Version, in.Note, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	sendPost(c, http.StatusOK, "post_changes_requested", p)
}

func (h *Handler) approvePost(c *gin.Context) {
	var u postURI
	var in versionInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	p, err := Approve(c.Request.Context(), h.Pool, u.id(), in.Version, actor(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	sendPost(c, http.StatusOK, "post_approved", p)
}

type scheduleInput struct {
	Version     int32     `json:"version" binding:"required,min=1"`
	ScheduledAt time.Time `json:"scheduledAt" binding:"required"`
}

func (h *Handler) schedulePost(c *gin.Context) {
	var u postURI
	var in scheduleInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	p, err := Schedule(ctx, h.Pool, u.id(), in.Version, in.ScheduledAt, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.enqueue(ctx, ScheduledTask(p)...)
	sendPost(c, http.StatusOK, "post_scheduled", p)
}

func (h *Handler) unschedulePost(c *gin.Context) {
	var u postURI
	var in versionInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	p, err := Unschedule(c.Request.Context(), h.Pool, u.id(), in.Version, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	sendPost(c, http.StatusOK, "post_unscheduled", p)
}

// publishPost puts the website article live at once and leaves each social channel to the worker.
func (h *Handler) publishPost(c *gin.Context) {
	var u postURI
	var in versionInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	p, err := PublishNow(ctx, h.Pool, u.id(), in.Version, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.enqueue(ctx, append(PublishTasks(p), RevalidateTasks(p)...)...)
	sendPost(c, http.StatusOK, "post_publishing", p)
}

func (h *Handler) archivePost(c *gin.Context) {
	var u postURI
	var in versionInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	p, err := Archive(ctx, h.Pool, u.id(), in.Version, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.enqueue(ctx, RevalidateTasks(p)...)
	sendPost(c, http.StatusOK, "post_archived", p)
}

type markPostedInput struct {
	Permalink string `json:"permalink" binding:"max=2000"`
}

// markPosted records a channel Daw Mi posted to herself.
func (h *Handler) markPosted(c *gin.Context) {
	var u channelURI
	var in markPostedInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	p, err := MarkPosted(c.Request.Context(), h.Pool, u.id(), u.Channel, in.Permalink, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	sendPost(c, http.StatusOK, "post_channel_marked_posted", p, "channel", u.Channel)
}

func (h *Handler) retryChannel(c *gin.Context) {
	var u channelURI
	if !web.BindURI(c, &u) {
		return
	}
	ctx := c.Request.Context()
	p, err := Retry(ctx, h.Pool, u.id(), u.Channel, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.enqueue(ctx, PublishTasks(p)...)
	sendPost(c, http.StatusOK, "post_channel_retried", p, "channel", u.Channel)
}
