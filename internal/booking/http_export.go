package booking

import (
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type exportAppointmentsQuery struct {
	From string `form:"from" binding:"required,datetime=2006-01-02"`
	To   string `form:"to" binding:"required,datetime=2006-01-02"`
}

func (h *Handler) exportAppointments(c *gin.Context) {
	var query exportAppointmentsQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	export, err := ExportAppointments(ctx, db.New(h.Pool), *dateOf(query.From), *dateOf(query.To))
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "appointments_exported",
		"request_id", web.RequestID(ctx),
		"user_id", currentUserID(c).String(),
		"from", query.From,
		"to", query.To,
		"rows", len(export.Rows),
	)

	filename := fmt.Sprintf("vetmimi-appointments-%s-%s.csv", query.From, query.To)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Status(http.StatusOK)
	err = writeExport(c.Writer, export)
	if err != nil {
		// The 200 has gone out, so a dropped connection can only be logged.
		slog.WarnContext(ctx, "appointments_export_cut_off", "request_id", web.RequestID(ctx), "err", err)
	}
}

// writeExport starts with a byte-order mark so Excel reads Burmese names as UTF-8.
func writeExport(w io.Writer, export Export) error {
	_, err := io.WriteString(w, "\uFEFF")
	if err != nil {
		return err
	}
	writer := csv.NewWriter(w)
	header := []string{"Reference", "Status", "Service", "Date", "Start time", "Timezone", "Duration (minutes)",
		"Format", "Client name", "Email", "Phone", "Practical note", "Late cancellation", "Created", "Last updated"}
	err = writer.Write(header)
	if err != nil {
		return err
	}
	for _, row := range export.Rows {
		err = writer.Write(newExportRecord(row, export))
		if err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func newExportRecord(row db.ExportAppointmentsRow, export Export) []string {
	startsAt := row.StartsAt.In(export.Location)
	lateCancellation := "no"
	if row.LateCancellation {
		lateCancellation = "yes"
	}
	record := []string{
		row.Reference,
		row.Status,
		row.ServiceName,
		startsAt.Format(time.DateOnly),
		startsAt.Format("15:04"),
		export.Timezone,
		strconv.Itoa(int(row.DurationMinutes)),
		row.Format,
		row.VisitorName,
		row.VisitorEmail,
		row.VisitorPhone.String,
		row.VisitorNote.String,
		lateCancellation,
		row.CreatedAt.In(export.Location).Format(time.DateTime),
		row.UpdatedAt.In(export.Location).Format(time.DateTime),
	}
	for i, cell := range record {
		record[i] = spreadsheetSafe(cell)
	}
	return record
}

// spreadsheetSafe stops a spreadsheet from running a cell as a formula.
func spreadsheetSafe(cell string) string {
	if cell == "" || !strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return cell
	}
	return "'" + cell
}
