package content

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type channelPath struct {
	Channel string `uri:"channel" binding:"required,oneof=facebook instagram linkedin"`
}

type versionBody struct {
	Version int32 `json:"version" binding:"required,min=1"`
}

type scheduleBody struct {
	Version     int32     `json:"version" binding:"required,min=1"`
	ScheduledAt time.Time `json:"scheduledAt" binding:"required"`
}

type markPostedBody struct {
	Permalink string `json:"permalink" binding:"max=2000"`
}

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

func (h *Handler) unschedulePost(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	var body versionBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	post, err := Unschedule(ctx, h.Pool, postID, body.Version, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logPost(ctx, "post_unscheduled", post)
	c.JSON(http.StatusOK, newPostResponse(post))
}

// publishPost puts the website article live at once and leaves each social channel to the worker.
func (h *Handler) publishPost(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	var body versionBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	post, err := PublishNow(ctx, h.Pool, postID, body.Version, currentUserID(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.Queue.Enqueue(ctx, PublishTasks(post)...)
	h.Queue.Enqueue(ctx, RevalidateTasks(post)...)
	logPost(ctx, "post_publishing", post)
	c.JSON(http.StatusOK, newPostResponse(post))
}

func (h *Handler) archivePost(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	var body versionBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	post, err := Archive(ctx, h.Pool, postID, body.Version, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.Queue.Enqueue(ctx, RevalidateTasks(post)...)
	logPost(ctx, "post_archived", post)
	c.JSON(http.StatusOK, newPostResponse(post))
}

// markPosted records a channel Daw Mi posted to herself.
func (h *Handler) markPosted(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	var path channelPath
	if !web.BindURI(c, &path) {
		return
	}
	var body markPostedBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	post, err := MarkPosted(ctx, h.Pool, postID, path.Channel, body.Permalink, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logPost(ctx, "post_channel_marked_posted", post, "channel", path.Channel)
	c.JSON(http.StatusOK, newPostResponse(post))
}

func (h *Handler) retryChannel(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	var path channelPath
	if !web.BindURI(c, &path) {
		return
	}
	ctx := c.Request.Context()

	post, err := Retry(ctx, h.Pool, postID, path.Channel, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.Queue.Enqueue(ctx, PublishTasks(post)...)
	logPost(ctx, "post_channel_retried", post, "channel", path.Channel)
	c.JSON(http.StatusOK, newPostResponse(post))
}
