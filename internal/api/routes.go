package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters" // register built-in filters via init() + DB-backed factories
	"gorm.io/gorm"
)

// NewRouter creates and configures the chi router with all API routes.
func NewRouter(db *gorm.DB, jwtService *auth.JWTService) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Initialize handlers
	healthH := handlers.NewHealthHandler(db)
	authH := handlers.NewAuthHandler(db, jwtService)
	domainH := handlers.NewDomainHandler(db)
	mailboxH := handlers.NewMailboxHandler(db)
	aliasH := handlers.NewAliasHandler(db)
	broker := handlers.NewSSEBroker()
	eventH := handlers.NewEventHandler(db, broker, jwtService)
	accountH := handlers.NewAccountHandler(db)
	searchH := handlers.NewSearchHandler(db)
	webmailH := handlers.NewWebmailAccountHandler(db)
	restmailH := handlers.NewRestmailHandler(db)

	// Register DB-backed filters that need a database connection.
	pipeline.DefaultRegistry.Register("greylist", filters.NewGreylist(db))
	pipeline.DefaultRegistry.Register("vacation", filters.NewVacation(db))
	pipeline.DefaultRegistry.Register("domain_allowlist", filters.NewDomainAllowlist(db))
	pipeline.DefaultRegistry.Register("contact_whitelist", filters.NewContactWhitelist(db))
	pipeline.DefaultRegistry.Register("recipient_check", filters.NewRecipientCheck(db))
	pipeline.DefaultRegistry.Register("sender_verify", filters.NewSenderVerify(db))

	pipelineEngine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default())
	messageH := handlers.NewMessageHandler(db, broker, pipelineEngine)
	pipelineH := handlers.NewPipelineHandler(db, pipelineEngine)

	// ═══════════════════════════════════════════════════════════════
	// Health (no auth)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/health", healthH.Health)

	// ═══════════════════════════════════════════════════════════════
	// Auth (no auth)
	// ═══════════════════════════════════════════════════════════════
	r.Post("/api/v1/auth/login", authH.Login)
	r.Post("/api/v1/auth/logout", authH.Logout)
	r.Post("/api/v1/auth/refresh", authH.Refresh)

	// ═══════════════════════════════════════════════════════════════
	// Inbound delivery (used by gateway, internal auth)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/mailboxes", mailboxH.CheckAddress)
	r.Post("/api/v1/messages/deliver", messageH.DeliverMessage)

	// ═══════════════════════════════════════════════════════════════
	// RESTMAIL server-to-server (unauthenticated, verified by DKIM/SPF)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/restmail/capabilities", restmailH.Capabilities)
	r.Get("/restmail/mailboxes", restmailH.CheckMailbox)
	r.Post("/restmail/messages", restmailH.Deliver)

	// ═══════════════════════════════════════════════════════════════
	// SSE (query-param auth, outside JWT middleware group)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/v1/accounts/{id}/events", eventH.Events)

	// ═══════════════════════════════════════════════════════════════
	// Authenticated routes (mail server operations)
	// ═══════════════════════════════════════════════════════════════
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTMiddleware(jwtService))

		// Linked accounts
		r.Get("/api/v1/accounts", accountH.ListAccounts)
		r.Get("/api/v1/accounts/{id}", accountH.GetAccount)
		r.Post("/api/v1/accounts", accountH.LinkAccount)
		r.Delete("/api/v1/accounts/{id}", accountH.UnlinkAccount)
		r.Post("/api/v1/accounts/test-connection", accountH.TestConnection)

		// Folders
		r.Get("/api/v1/accounts/{id}/folders", messageH.ListFolders)

		// Messages
		r.Get("/api/v1/accounts/{id}/folders/{folder}/messages", messageH.ListMessages)
		r.Get("/api/v1/messages/{id}", messageH.GetMessage)
		r.Patch("/api/v1/messages/{id}", messageH.UpdateMessage)
		r.Delete("/api/v1/messages/{id}", messageH.DeleteMessage)
		r.Post("/api/v1/messages/send", messageH.SendMessage)

		// Search
		r.Get("/api/v1/accounts/{id}/search", searchH.Search)

		// Quarantine
		r.Get("/api/v1/accounts/{id}/quarantine", pipelineH.ListQuarantine)
		r.Post("/api/v1/accounts/{id}/quarantine/{mid}/release", pipelineH.ReleaseQuarantine)
		r.Delete("/api/v1/accounts/{id}/quarantine/{mid}", pipelineH.DeleteQuarantine)
	})

	// ═══════════════════════════════════════════════════════════════
	// Admin routes
	// ═══════════════════════════════════════════════════════════════
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTMiddleware(jwtService))
		r.Use(middleware.AdminOnly)

		// Domains
		r.Get("/api/v1/admin/domains", domainH.List)
		r.Post("/api/v1/admin/domains", domainH.Create)
		r.Get("/api/v1/admin/domains/{id}", domainH.Get)
		r.Patch("/api/v1/admin/domains/{id}", domainH.Update)
		r.Delete("/api/v1/admin/domains/{id}", domainH.Delete)

		// Mailboxes
		r.Get("/api/v1/admin/mailboxes", mailboxH.List)
		r.Post("/api/v1/admin/mailboxes", mailboxH.Create)
		r.Get("/api/v1/admin/mailboxes/{id}", mailboxH.Get)
		r.Patch("/api/v1/admin/mailboxes/{id}", mailboxH.Update)
		r.Delete("/api/v1/admin/mailboxes/{id}", mailboxH.Delete)

		// Aliases
		r.Get("/api/v1/admin/aliases", aliasH.List)
		r.Post("/api/v1/admin/aliases", aliasH.Create)
		r.Get("/api/v1/admin/aliases/{id}", aliasH.Get)
		r.Patch("/api/v1/admin/aliases/{id}", aliasH.Update)
		r.Delete("/api/v1/admin/aliases/{id}", aliasH.Delete)

		// Webmail accounts
		r.Get("/api/v1/admin/webmail-accounts", webmailH.List)
		r.Post("/api/v1/admin/webmail-accounts", webmailH.Create)
		r.Get("/api/v1/admin/webmail-accounts/{id}", webmailH.Get)
		r.Delete("/api/v1/admin/webmail-accounts/{id}", webmailH.Delete)

		// Pipelines
		r.Get("/api/v1/admin/pipelines", pipelineH.ListPipelines)
		r.Post("/api/v1/admin/pipelines", pipelineH.CreatePipeline)
		r.Patch("/api/v1/admin/pipelines/{id}", pipelineH.UpdatePipeline)
		r.Delete("/api/v1/admin/pipelines/{id}", pipelineH.DeletePipeline)
		r.Post("/api/v1/admin/pipelines/test", pipelineH.TestPipeline)
		r.Post("/api/v1/admin/pipelines/test-filter", pipelineH.TestFilter)

		// Custom filters
		r.Get("/api/v1/admin/custom-filters", pipelineH.ListCustomFilters)
		r.Post("/api/v1/admin/custom-filters", pipelineH.CreateCustomFilter)
		r.Delete("/api/v1/admin/custom-filters/{id}", pipelineH.DeleteCustomFilter)
	})

	// ═══════════════════════════════════════════════════════════════
	// Test/diagnostic endpoints (development only)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/test/db/domains", func(w http.ResponseWriter, r *http.Request) {
		var domains []models.Domain
		db.Find(&domains)
		respond.Data(w, http.StatusOK, domains)
	})
	r.Get("/api/test/db/mailboxes", func(w http.ResponseWriter, r *http.Request) {
		var mailboxes []models.Mailbox
		db.Preload("Domain").Find(&mailboxes)
		respond.Data(w, http.StatusOK, mailboxes)
	})
	r.Get("/api/test/db/messages", func(w http.ResponseWriter, r *http.Request) {
		var messages []models.Message
		query := db.Model(&models.Message{})
		if mailboxID := r.URL.Query().Get("mailbox_id"); mailboxID != "" {
			query = query.Where("mailbox_id = ?", mailboxID)
		}
		if folder := r.URL.Query().Get("folder"); folder != "" {
			query = query.Where("folder = ?", folder)
		}
		query.Order("received_at DESC").Limit(100).Find(&messages)
		respond.Data(w, http.StatusOK, messages)
	})

	return r
}
