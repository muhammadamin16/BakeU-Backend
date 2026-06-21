package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"bakeu/internal/email"
	"bakeu/internal/middleware"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"bakeu/internal/storage"
)

// MarketHandler serves bakery-market application and the email-driven approval
// review flow.
type MarketHandler struct {
	markets      *repository.MarketRepo
	users        *repository.UserRepo
	store        *storage.Store
	mailer       *email.Mailer
	adminEmails  []string
	appBaseURL   string
	reviewSecret string
}

func NewMarketHandler(
	markets *repository.MarketRepo,
	users *repository.UserRepo,
	store *storage.Store,
	mailer *email.Mailer,
	adminEmails []string,
	appBaseURL string,
	reviewSecret string,
) *MarketHandler {
	return &MarketHandler{
		markets:      markets,
		users:        users,
		store:        store,
		mailer:       mailer,
		adminEmails:  adminEmails,
		appBaseURL:   appBaseURL,
		reviewSecret: reviewSecret,
	}
}

// Mine handles GET /api/market/mine (seller only) — the caller's own market in
// any status, or 404 if they have not applied yet.
func (h *MarketHandler) Mine(c *gin.Context) {
	m, err := h.markets.GetBySeller(c.Request.Context(), middleware.UID(c))
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no market yet"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load market"})
		return
	}
	c.JSON(http.StatusOK, m)
}

// Apply handles POST /api/market (seller only) — submit or re-submit a bakery
// application for review. Accepts multipart form fields plus a required `logo`
// file. Creating is allowed when the seller has no market, or when their
// previous application was rejected (a new submission resets it to pending). An
// already-approved market cannot be re-submitted here.
func (h *MarketHandler) Apply(c *gin.Context) {
	uid := middleware.UID(c)
	ctx := c.Request.Context()

	name := c.PostForm("name")
	description := c.PostForm("description")
	phone := c.PostForm("phone")
	emailAddr := c.PostForm("email")
	city := c.PostForm("city")
	area := c.PostForm("area")
	fulfillment := models.Fulfillment(c.PostForm("fulfillment"))

	if name == "" || description == "" || phone == "" || emailAddr == "" || city == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, description, phone, email and city are required"})
		return
	}
	if !fulfillment.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fulfillment must be 'pickup', 'delivery' or 'both'"})
		return
	}

	// Existing market controls whether this is a create or a resubmit.
	existing, err := h.markets.GetBySeller(ctx, uid)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		// create
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load market"})
		return
	case existing.Status == models.MarketApproved:
		c.JSON(http.StatusConflict, gin.H{"error": "your market is already approved"})
		return
	}

	logoURL, ok := uploadImageFile(c, h.store, "logo")
	if !ok {
		return
	}

	now := time.Now().UTC()
	if existing == nil {
		m := &models.Market{
			SellerID:    uid,
			Name:        name,
			Description: description,
			Phone:       phone,
			Email:       emailAddr,
			City:        city,
			Area:        area,
			Fulfillment: fulfillment,
			LogoURL:     logoURL,
			Status:      models.MarketPending,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := h.markets.Create(ctx, m); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create market"})
			return
		}
		h.notifyAdmins(ctx, m)
		c.JSON(http.StatusCreated, m)
		return
	}

	// Resubmit: update the existing (pending or rejected) application.
	existing.Name = name
	existing.Description = description
	existing.Phone = phone
	existing.Email = emailAddr
	existing.City = city
	existing.Area = area
	existing.Fulfillment = fulfillment
	existing.LogoURL = logoURL
	existing.Status = models.MarketPending
	existing.RejectionReason = ""
	existing.UpdatedAt = now
	if err := h.markets.Update(ctx, existing); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update market"})
		return
	}
	h.notifyAdmins(ctx, existing)
	c.JSON(http.StatusOK, existing)
}

// ─────────────────────────── Admin review (email) ──────────────────────────

// reviewToken is the HMAC that authorizes acting on a specific market via the
// email links. It is unguessable without MarketReviewSecret.
func (h *MarketHandler) reviewToken(marketID string) string {
	mac := hmac.New(sha256.New, []byte(h.reviewSecret))
	mac.Write([]byte(marketID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *MarketHandler) validToken(marketID, token string) bool {
	want := h.reviewToken(marketID)
	return hmac.Equal([]byte(want), []byte(token))
}

// ReviewPage handles GET /api/market/review/:id?token=... — a small HTML page
// (no app auth; gated by the HMAC token) showing the application with Approve /
// Decline buttons. GET only displays, so email link prefetchers cannot mutate.
func (h *MarketHandler) ReviewPage(c *gin.Context) {
	id := c.Param("id")
	if !h.validToken(id, c.Query("token")) {
		c.Data(http.StatusForbidden, "text/html; charset=utf-8", []byte("<h1>Invalid or expired link</h1>"))
		return
	}
	m, err := h.markets.Get(c.Request.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		c.Data(http.StatusNotFound, "text/html; charset=utf-8", []byte("<h1>Application not found</h1>"))
		return
	}
	if err != nil {
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte("<h1>Something went wrong</h1>"))
		return
	}

	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	logoURL := ""
	if m.LogoURL != "" {
		logoURL = h.appBaseURL + m.LogoURL
	}
	if err := reviewPageTmpl.Execute(c.Writer, map[string]any{
		"Market":  m,
		"Token":   c.Query("token"),
		"Action":  h.appBaseURL + "/api/market/review/" + m.ID,
		"LogoURL": logoURL,
		"Done":    m.Status != models.MarketPending,
	}); err != nil {
		log.Printf("[market] render review page: %v", err)
	}
}

// ReviewDecision handles POST /api/market/review/:id — applies the admin's
// Approve/Decline from the review page. Mutating via POST keeps it safe from
// link prefetchers. Reads form fields: token, decision (approve|decline), reason.
func (h *MarketHandler) ReviewDecision(c *gin.Context) {
	id := c.Param("id")
	if !h.validToken(id, c.PostForm("token")) {
		c.Data(http.StatusForbidden, "text/html; charset=utf-8", []byte("<h1>Invalid or expired link</h1>"))
		return
	}
	ctx := c.Request.Context()
	m, err := h.markets.Get(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		c.Data(http.StatusNotFound, "text/html; charset=utf-8", []byte("<h1>Application not found</h1>"))
		return
	}
	if err != nil {
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte("<h1>Something went wrong</h1>"))
		return
	}
	if m.Status != models.MarketPending {
		c.Data(http.StatusConflict, "text/html; charset=utf-8",
			[]byte("<h1>Already "+html.EscapeString(string(m.Status))+"</h1>"))
		return
	}

	decision := c.PostForm("decision")
	switch decision {
	case "approve":
		m.Status = models.MarketApproved
		m.RejectionReason = ""
	case "decline":
		m.Status = models.MarketRejected
		m.RejectionReason = c.PostForm("reason")
	default:
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte("<h1>Unknown decision</h1>"))
		return
	}
	m.ReviewedAt = time.Now().UTC()
	m.UpdatedAt = m.ReviewedAt
	if err := h.markets.Update(ctx, m); err != nil {
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte("<h1>Failed to save decision</h1>"))
		return
	}

	h.notifySeller(ctx, m)

	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = decisionDoneTmpl.Execute(c.Writer, map[string]any{"Market": m})
}

// ─────────────────────────────── Emails ────────────────────────────────────

func (h *MarketHandler) notifyAdmins(ctx context.Context, m *models.Market) {
	if len(h.adminEmails) == 0 {
		return
	}
	reviewURL := fmt.Sprintf("%s/api/market/review/%s?token=%s", h.appBaseURL, m.ID, h.reviewToken(m.ID))
	logoURL := ""
	if m.LogoURL != "" {
		logoURL = h.appBaseURL + m.LogoURL
	}
	body := adminEmailHTML(m, reviewURL, logoURL)
	subject := "New bakery application: " + m.Name
	if err := h.mailer.Send(ctx, h.adminEmails, subject, body); err != nil {
		log.Printf("[market] notify admins: %v", err)
	}
}

func (h *MarketHandler) notifySeller(ctx context.Context, m *models.Market) {
	if m.Email == "" {
		return
	}
	var subject string
	if m.Status == models.MarketApproved {
		subject = "Your bakery is approved 🎉"
	} else {
		subject = "Update on your bakery application"
	}
	if err := h.mailer.Send(ctx, []string{m.Email}, subject, sellerEmailHTML(m)); err != nil {
		log.Printf("[market] notify seller: %v", err)
	}
}

func adminEmailHTML(m *models.Market, reviewURL, logoURL string) string {
	e := html.EscapeString
	logoTag := ""
	if logoURL != "" {
		logoTag = `<p><img src="` + e(logoURL) + `" alt="logo" style="max-width:160px;border-radius:12px"/></p>`
	}
	return fmt.Sprintf(`
<div style="font-family:sans-serif;max-width:560px;margin:auto">
  <h2>New bakery application</h2>
  <p><b>%s</b> wants to open a bakery on Bakeu.</p>
  <table cellpadding="6" style="border-collapse:collapse">
    <tr><td><b>Name</b></td><td>%s</td></tr>
    <tr><td><b>Description</b></td><td>%s</td></tr>
    <tr><td><b>Phone</b></td><td>%s</td></tr>
    <tr><td><b>Email</b></td><td>%s</td></tr>
    <tr><td><b>Location</b></td><td>%s, %s</td></tr>
    <tr><td><b>Fulfillment</b></td><td>%s</td></tr>
  </table>
  %s
  <p><a href="%s" style="background:#E9A319;color:#1A0F00;padding:12px 20px;border-radius:10px;text-decoration:none;font-weight:600">Review application →</a></p>
  <p style="color:#888;font-size:12px">Opens a page where you can Approve or Decline.</p>
</div>`,
		e(m.Name), e(m.Name), e(m.Description), e(m.Phone), e(m.Email),
		e(m.City), e(m.Area), e(string(m.Fulfillment)), logoTag, reviewURL)
}

func sellerEmailHTML(m *models.Market) string {
	e := html.EscapeString
	if m.Status == models.MarketApproved {
		return fmt.Sprintf(`
<div style="font-family:sans-serif;max-width:560px;margin:auto">
  <h2>Your bakery is live 🎉</h2>
  <p><b>%s</b> has been approved. Open the Bakeu app to add products and start receiving orders.</p>
</div>`, e(m.Name))
	}
	reason := m.RejectionReason
	if reason == "" {
		reason = "No specific reason was provided."
	}
	return fmt.Sprintf(`
<div style="font-family:sans-serif;max-width:560px;margin:auto">
  <h2>About your bakery application</h2>
  <p>Unfortunately <b>%s</b> wasn't approved this time.</p>
  <p><b>Reason:</b> %s</p>
  <p>You can update your details in the app and re-submit.</p>
</div>`, e(m.Name), e(reason))
}

var reviewPageTmpl = template.Must(template.New("review").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Review bakery</title></head>
<body style="font-family:sans-serif;background:#1A0F00;color:#fff;margin:0;padding:24px">
<div style="max-width:520px;margin:auto;background:#2A1A0A;border-radius:18px;padding:24px">
  <h2 style="color:#E9A319;margin-top:0">{{.Market.Name}}</h2>
  {{if .LogoURL}}<img src="{{.LogoURL}}" alt="" style="max-width:120px;border-radius:12px" onerror="this.style.display='none'"/>{{end}}
  <p>{{.Market.Description}}</p>
  <p style="color:#bbb">📞 {{.Market.Phone}} · ✉️ {{.Market.Email}}</p>
  <p style="color:#bbb">📍 {{.Market.City}}{{if .Market.Area}}, {{.Market.Area}}{{end}} · {{.Market.Fulfillment}}</p>
  {{if .Done}}
    <p style="color:#E9A319">This application is already <b>{{.Market.Status}}</b>.</p>
  {{else}}
  <form method="POST" action="{{.Action}}">
    <input type="hidden" name="token" value="{{.Token}}"/>
    <textarea name="reason" placeholder="Reason (only needed if declining)" style="width:100%;box-sizing:border-box;height:70px;border-radius:10px;padding:10px;border:none;margin:12px 0"></textarea>
    <div style="display:flex;gap:12px">
      <button name="decision" value="approve" style="flex:1;background:#3DDC84;color:#06301a;border:none;padding:14px;border-radius:12px;font-weight:700;font-size:16px">Approve</button>
      <button name="decision" value="decline" style="flex:1;background:#E5484D;color:#fff;border:none;padding:14px;border-radius:12px;font-weight:700;font-size:16px">Decline</button>
    </div>
  </form>
  {{end}}
</div>
</body></html>`))

var decisionDoneTmpl = template.Must(template.New("done").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Done</title></head>
<body style="font-family:sans-serif;background:#1A0F00;color:#fff;text-align:center;padding:60px">
  <h1 style="color:#E9A319">{{.Market.Name}} — {{.Market.Status}}</h1>
  <p>The seller has been notified. You can close this page.</p>
</body></html>`))
