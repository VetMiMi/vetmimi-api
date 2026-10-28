package video

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type roomResponse struct {
	ID        pgtype.UUID `json:"id"`
	State     string      `json:"state"`
	OpensAt   time.Time   `json:"opensAt"`
	ClosesAt  time.Time   `json:"closesAt"`
	StartedAt *time.Time  `json:"startedAt,omitempty"`
	EndedAt   *time.Time  `json:"endedAt,omitempty"`
}

// startSession gives Daw Mi her ticket while the admin offers start_video.
func (h *Handler) startSession(c *gin.Context) {
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	t, err := JoinAsPractitioner(ctx, db.New(h.Pool), h.Issuer, id, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	logTicket(ctx, t)
	c.JSON(http.StatusCreated, t)
}

// endSession closes both sockets. The appointment keeps its status until
// Daw Mi marks it completed or a no-show.
func (h *Handler) endSession(c *gin.Context) {
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	room, err := EndAsPractitioner(ctx, db.New(h.Pool), id, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.Hub.EndRoom(room.ID)
	slog.InfoContext(ctx, "video_session_ended", "request_id", web.RequestID(ctx), "room_id", room.ID.String())
	c.JSON(http.StatusOK, roomResponse{
		ID:        room.ID,
		State:     room.State,
		OpensAt:   room.OpensAt.UTC(),
		ClosesAt:  room.ClosesAt.UTC(),
		StartedAt: utcOrNil(room.StartedAt),
		EndedAt:   utcOrNil(room.EndedAt),
	})
}

func utcOrNil(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	utc := t.Time.UTC()
	return &utc
}
