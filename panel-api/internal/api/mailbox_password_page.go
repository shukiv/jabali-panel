package api

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// mailboxPasswordPath is the mailbox password page (GH #2543), where a mailbox
// user who has no panel login changes their own password by proving the
// current one. nginx forwards it from every mail hostname with webmail on;
// the panel hostname reaches it too, as it reaches every panel-api path. The
// page doesn't depend on the host it is reached on.
const mailboxPasswordPath = "/mailbox-password"

// mailboxPasswordBodyLimit bounds the form: four short fields.
const mailboxPasswordBodyLimit = 8 << 10

// MailboxPasswordLimiter spends one try at the page from the request's address
// and the mailbox the form names; false means over budget.
type MailboxPasswordLimiter interface {
	AllowMailboxPassword(c *gin.Context, email string, log *slog.Logger) bool
}

// MailboxPasswordPageConfig wires the mailbox password page. Everything but
// SSOKey, MailCredentials and Agent is required, the limiter above all:
// without one the page answers 503 rather than check passwords unthrottled.
type MailboxPasswordPageConfig struct {
	Mailboxes repository.MailboxRepository
	Domains   repository.DomainRepository
	Users     repository.UserRepository
	// SSOKey re-seals the new password for webmail sign-in from the panel,
	// as a change in the panel does.
	SSOKey *ssokey.Key
	// MailCredentials removes the app passwords the mailbox made before the
	// change, as a change in the panel does.
	MailCredentials userops.MailCredentialSweeper
	Agent           agent.AgentInterface
	Limiter         MailboxPasswordLimiter
	Log             *slog.Logger
}

// RegisterMailboxPasswordRoutes mounts the page at the engine root, like
// /sso/webmail, outside /api/v1 and its session auth.
func RegisterMailboxPasswordRoutes(r gin.IRouter, cfg MailboxPasswordPageConfig) {
	h := &mailboxPasswordPage{cfg: cfg}
	r.GET(mailboxPasswordPath, h.show)
	r.POST(mailboxPasswordPath, h.submit)
}

type mailboxPasswordPage struct{ cfg MailboxPasswordPageConfig }

type mailboxPasswordView struct {
	Email   string
	Error   string
	Done    bool
	MinLen  int
	MaxByte int
}

const mailboxPasswordUnavailable = "Changing a mailbox password here is not available on this server."

func (h *mailboxPasswordPage) log() *slog.Logger {
	if h.cfg.Log != nil {
		return h.cfg.Log
	}
	return slog.Default()
}

func (h *mailboxPasswordPage) configured() bool {
	return h.cfg.Mailboxes != nil && h.cfg.Domains != nil && h.cfg.Users != nil && h.cfg.Limiter != nil
}

func (h *mailboxPasswordPage) show(c *gin.Context) {
	if !h.configured() {
		renderMailboxPasswordPage(c, http.StatusServiceUnavailable, mailboxPasswordView{Error: mailboxPasswordUnavailable})
		return
	}
	renderMailboxPasswordPage(c, http.StatusOK, mailboxPasswordView{})
}

func (h *mailboxPasswordPage) submit(c *gin.Context) {
	if !h.configured() {
		renderMailboxPasswordPage(c, http.StatusServiceUnavailable, mailboxPasswordView{Error: mailboxPasswordUnavailable})
		return
	}
	if mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
		renderMailboxPasswordPage(c, http.StatusUnsupportedMediaType, mailboxPasswordView{Error: "Send the form from this page."})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, mailboxPasswordBodyLimit)
	if err := c.Request.ParseForm(); err != nil {
		renderMailboxPasswordPage(c, http.StatusBadRequest, mailboxPasswordView{Error: "The form could not be read. Try again."})
		return
	}
	// Body fields only: a password in the query string would end up in logs.
	form := c.Request.PostForm
	email := strings.TrimSpace(form.Get("email"))
	current := form.Get("current_password")
	newPw := form.Get("new_password")
	confirm := form.Get("confirm_password")
	view := mailboxPasswordView{Email: email}

	if !h.cfg.Limiter.AllowMailboxPassword(c, email, h.log()) {
		c.Header("Retry-After", "60")
		view.Error = "Too many tries. Wait a minute, then try again."
		renderMailboxPasswordPage(c, http.StatusTooManyRequests, view)
		return
	}
	if newPw != confirm {
		view.Error = "The new password and its confirmation don't match."
		renderMailboxPasswordPage(c, http.StatusBadRequest, view)
		return
	}
	if newPw != "" && newPw == current {
		view.Error = "The new password must be different from the current one."
		renderMailboxPasswordPage(c, http.StatusBadRequest, view)
		return
	}

	ctx := c.Request.Context()
	mb, err := mailboxops.ChangeOwnPassword(ctx, mailboxops.Deps{
		Mailboxes: h.cfg.Mailboxes,
		SSOKey:    h.cfg.SSOKey,
	}, h.eligible, mailboxops.ChangeOwnPasswordInput{
		Email:           email,
		CurrentPassword: current,
		NewPassword:     newPw,
	}, h.notifyAgent)
	ip := middleware.RealClientIP(c)
	switch {
	case err == nil:
	case errors.Is(err, mailboxops.ErrWeakPassword):
		view.Error = "The new password needs at least 8 characters, and at most 72 bytes."
		renderMailboxPasswordPage(c, http.StatusBadRequest, view)
		return
	case errors.Is(err, mailboxops.ErrSelfServiceRefused):
		h.log().Info("mailbox password page: change refused", "client_ip", ip)
		view.Error = "The email address or the current password is wrong, or this mailbox's password can't be changed here. Your hosting provider can change it for you."
		renderMailboxPasswordPage(c, http.StatusBadRequest, view)
		return
	default:
		h.log().Error("mailbox password page: change failed", "client_ip", ip, "err", err)
		view.Error = "The password could not be changed. Try again later."
		renderMailboxPasswordPage(c, http.StatusInternalServerError, view)
		return
	}
	h.log().Info("mailbox password page: password changed", "mailbox_id", mb.ID, "client_ip", ip)

	// As after a change in the panel: remove the app passwords the mailbox
	// made before the change, then flush the login cache that would still
	// answer them. Best-effort; the reconciler's mail.credentials pass is the
	// backstop.
	if h.cfg.MailCredentials != nil {
		sctx, cancel := context.WithTimeout(ctx, mailboxAgentTimeout)
		removed, serr := h.cfg.MailCredentials.SweepMailCredentials(sctx)
		cancel()
		logMailCredentialRemovals(removed, "mailbox password page")
		if serr != nil {
			h.log().Warn("mailbox password page: mail credentials sweep failed", "err", serr)
		}
		if len(removed) > 0 {
			h.notifyAgent(ctx, "mail.auth_cache.flush", map[string]any{})
		}
	}
	renderMailboxPasswordPage(c, http.StatusOK, mailboxPasswordView{Done: true})
}

// eligible mirrors the mail server's sign-in rule (Stalwart's queryLogin):
// the domain's ownership is verified and its owner is not suspended. A
// missing domain or owner refuses.
func (h *mailboxPasswordPage) eligible(ctx context.Context, mb *models.Mailbox) (bool, error) {
	dom, err := h.cfg.Domains.FindByID(ctx, mb.DomainID)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if dom.OwnershipStatus != models.OwnershipVerified {
		return false, nil
	}
	u, err := h.cfg.Users.FindByID(ctx, dom.UserID)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !u.Suspended, nil
}

func (h *mailboxPasswordPage) notifyAgent(ctx context.Context, command string, params any) {
	if h.cfg.Agent == nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, mailboxAgentTimeout)
	defer cancel()
	_, _ = h.cfg.Agent.Call(actx, command, params)
}

// renderMailboxPasswordPage writes the page with headers that keep it out of
// frames, caches and Referer headers, and let it load nothing from elsewhere.
func renderMailboxPasswordPage(c *gin.Context, status int, v mailboxPasswordView) {
	v.MinLen = mailboxops.PasswordMinChars
	v.MaxByte = mailboxops.PasswordMaxBytes
	h := c.Writer.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Pragma", "no-cache")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	c.Status(status)
	if err := mailboxPasswordTmpl.Execute(c.Writer, v); err != nil {
		slog.Warn("mailbox password page: render failed", "err", err)
	}
}

var mailboxPasswordTmpl = template.Must(template.New("mailbox-password").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Change mailbox password</title>
<style>
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center;padding:24px;box-sizing:border-box}
.card{width:100%;max-width:400px}
h1{font-size:20px;margin:0 0 8px}
p{color:#94a3b8;line-height:1.5;margin:0 0 20px}
label{display:block;font-size:14px;margin:0 0 6px}
input{width:100%;box-sizing:border-box;padding:10px 12px;margin:0 0 16px;border:1px solid #334155;border-radius:8px;background:#1e293b;color:#e2e8f0;font-size:15px}
button{display:inline-block;width:100%;box-sizing:border-box;text-align:center;background:#2563eb;color:#fff;border:0;text-decoration:none;padding:11px 18px;border-radius:8px;font-weight:600;font-size:15px;cursor:pointer}
.err{background:#450a0a;border:1px solid #7f1d1d;color:#fecaca;border-radius:8px;padding:10px 12px;margin:0 0 16px;line-height:1.4}
.ok{background:#052e16;border:1px solid #166534;color:#bbf7d0;border-radius:8px;padding:10px 12px;margin:0 0 20px;line-height:1.4}
small{display:block;color:#94a3b8;margin:-10px 0 16px}
</style></head><body><div class="card">
<h1>Change mailbox password</h1>
{{if .Done}}<div class="ok" role="status">Your password was changed. Sign in again with the new password in webmail and in every mail app.</div>
{{else}}<p>Enter your email address and current password, then choose a new password.</p>
{{if .Error}}<div class="err" role="alert">{{.Error}}</div>{{end}}
<form method="post" action="/mailbox-password" autocomplete="on">
<label for="email">Email address</label>
<input id="email" name="email" type="email" autocomplete="username" required maxlength="320" value="{{.Email}}">
<label for="current_password">Current password</label>
<input id="current_password" name="current_password" type="password" autocomplete="current-password" required maxlength="{{.MaxByte}}">
<label for="new_password">New password</label>
<input id="new_password" name="new_password" type="password" autocomplete="new-password" required minlength="{{.MinLen}}" maxlength="{{.MaxByte}}">
<small>At least {{.MinLen}} characters.</small>
<label for="confirm_password">Confirm new password</label>
<input id="confirm_password" name="confirm_password" type="password" autocomplete="new-password" required minlength="{{.MinLen}}" maxlength="{{.MaxByte}}">
<button type="submit">Change password</button>
</form>{{end}}
</div></body></html>
`))
