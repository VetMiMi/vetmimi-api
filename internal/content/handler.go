package content

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Pool           *pgxpool.Pool
	Queue          *queue.Queue // nil in tests: tasks are dropped
	MediaPublicURL string
	Now            clock.Now
}

// Routes: editors write and submit; only a site administrator reviews and publishes.
func (h *Handler) Routes(r web.Routes) {
	editor := r.Admin.Group("", web.Role(auth.ContentEditor))
	editor.GET("/admin/posts", h.listPosts)
	editor.POST("/admin/posts", h.createPost)
	editor.GET("/admin/posts/:postId", h.getPost)
	editor.PATCH("/admin/posts/:postId", h.updatePost)
	editor.DELETE("/admin/posts/:postId", h.deletePost)
	editor.POST("/admin/posts/:postId/submit", h.submitPost)
	editor.POST("/admin/posts/:postId/channels/:channel/mark-posted", h.markPosted)
	editor.POST("/admin/posts/:postId/channels/:channel/retry", h.retryChannel)

	reviewer := r.Admin.Group("", web.Role(auth.SiteAdmin))
	reviewer.POST("/admin/posts/:postId/request-changes", h.requestChanges)
	reviewer.POST("/admin/posts/:postId/approve", h.approvePost)
	reviewer.POST("/admin/posts/:postId/schedule", h.schedulePost)
	reviewer.POST("/admin/posts/:postId/unschedule", h.unschedulePost)
	reviewer.POST("/admin/posts/:postId/publish", h.publishPost)
	reviewer.POST("/admin/posts/:postId/archive", h.archivePost)

	r.Public.GET("/public/articles", h.listArticles)
	r.Public.GET("/public/articles/:slug", h.getArticle)
}

func (h *Handler) enqueue(ctx context.Context, tasks ...queue.Task) {
	if h.Queue != nil {
		h.Queue.Enqueue(ctx, tasks...)
	}
}
