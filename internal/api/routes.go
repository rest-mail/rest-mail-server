package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters" // register built-in filters via init() + DB-backed factories
	"gorm.io/gorm"
)

// NewRouter creates and configures the chi router with all API routes.
func NewRouter(db *gorm.DB, jwtService *auth.JWTService, cfg *config.Config) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSAllowedOrigins,
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
	attachmentH := handlers.NewAttachmentHandler(db)
	contactH := handlers.NewContactHandler(db)
	vacationH := handlers.NewVacationHandler(db)
	sieveH := handlers.NewSieveHandler(db)
	senderRuleH := handlers.NewSenderRuleHandler(db)
	queueH := handlers.NewQueueHandler(db)
	banH := handlers.NewBanHandler(db)
	logH := handlers.NewLogHandler(db)
	dkimH := handlers.NewDKIMHandler(db, cfg.MasterKey)
	certH := handlers.NewCertificateHandler(db, cfg.MasterKey)
	testH := handlers.NewTestHandler(db, cfg)

	// Register DB-backed filters that need a database connection.
	pipeline.DefaultRegistry.Register("greylist", filters.NewGreylist(db))
	pipeline.DefaultRegistry.Register("vacation", filters.NewVacation(db))
	pipeline.DefaultRegistry.Register("domain_allowlist", filters.NewDomainAllowlist(db))
	pipeline.DefaultRegistry.Register("contact_whitelist", filters.NewContactWhitelist(db))
	pipeline.DefaultRegistry.Register("recipient_check", filters.NewRecipientCheck(db))
	pipeline.DefaultRegistry.Register("sender_verify", filters.NewSenderVerify(db))
	pipeline.DefaultRegistry.Register("dkim_sign", filters.NewDKIMSign(db, cfg.MasterKey))
	pipeline.DefaultRegistry.Register("arc_seal", filters.NewARCSeal(db, cfg.MasterKey))

	pipelineEngine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default())
	messageH := handlers.NewMessageHandler(db, broker, pipelineEngine)
	pipelineH := handlers.NewPipelineHandler(db, pipelineEngine)
	restmailH := handlers.NewRestmailHandler(db, pipelineEngine)

	// ═══════════════════════════════════════════════════════════════
	// API Documentation (no auth)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/docs", SwaggerUIHandler())
	r.Get("/api/docs/openapi.yaml", OpenAPISpecHandler())

	// ═══════════════════════════════════════════════════════════════
	// Health (no auth)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/health", healthH.Health)

	// ═══════════════════════════════════════════════════════════════
	// Email client auto-configuration (no auth)
	// ═══════════════════════════════════════════════════════════════
	autoconfigH := handlers.NewAutoconfigHandler(db)
	r.Get("/mail/config-v1.1.xml", autoconfigH.MozillaAutoconfig)
	r.Get("/.well-known/autoconfig/mail/config-v1.1.xml", autoconfigH.MozillaAutoconfig)
	r.Post("/autodiscover/autodiscover.xml", autoconfigH.MicrosoftAutodiscover)

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
		r.Post("/api/v1/accounts/{id}/folders", messageH.CreateFolder)
		r.Patch("/api/v1/accounts/{id}/folders/{folder}", messageH.RenameFolder)
		r.Delete("/api/v1/accounts/{id}/folders/{folder}", messageH.DeleteFolder)

		// Quota
		r.Get("/api/v1/accounts/{id}/quota", messageH.GetQuota)

		// Messages
		r.Get("/api/v1/accounts/{id}/folders/{folder}/messages", messageH.ListMessages)
		r.Get("/api/v1/messages/{id}", messageH.GetMessage)
		r.Patch("/api/v1/messages/{id}", messageH.UpdateMessage)
		r.Delete("/api/v1/messages/{id}", messageH.DeleteMessage)
		r.Post("/api/v1/messages/send", messageH.SendMessage)
		r.Get("/api/v1/messages/{id}/raw", messageH.GetRawMessage)
		r.Post("/api/v1/messages/{id}/forward", messageH.ForwardMessage)

		// Drafts
		r.Post("/api/v1/messages/draft", messageH.SaveDraft)
		r.Put("/api/v1/messages/draft/{id}", messageH.UpdateDraft)
		r.Post("/api/v1/messages/draft/{id}/send", messageH.SendDraft)

		// Threads
		r.Get("/api/v1/accounts/{id}/threads/{threadID}", messageH.GetThread)

		// Attachments
		r.Get("/api/v1/attachments/{id}", attachmentH.GetAttachment)
		r.Get("/api/v1/messages/{id}/attachments", attachmentH.ListAttachments)

		// Contacts
		r.Get("/api/v1/accounts/{id}/contacts", contactH.ListContacts)
		r.Post("/api/v1/accounts/{id}/contacts", contactH.CreateContact)
		r.Patch("/api/v1/accounts/{id}/contacts/{cid}", contactH.UpdateContact)
		r.Delete("/api/v1/accounts/{id}/contacts/{cid}", contactH.DeleteContact)
		r.Post("/api/v1/accounts/{id}/contacts/block", contactH.BlockSender)
		r.Post("/api/v1/accounts/{id}/contacts/import", contactH.ImportContacts)

		// Vacation
		r.Get("/api/v1/accounts/{id}/vacation", vacationH.GetVacation)
		r.Put("/api/v1/accounts/{id}/vacation", vacationH.SetVacation)
		r.Delete("/api/v1/accounts/{id}/vacation", vacationH.DisableVacation)

		// Sieve scripts
		r.Get("/api/v1/accounts/{id}/sieve", sieveH.GetScript)
		r.Put("/api/v1/accounts/{id}/sieve", sieveH.PutScript)
		r.Delete("/api/v1/accounts/{id}/sieve", sieveH.DeleteScript)
		r.Post("/api/v1/accounts/{id}/sieve/validate", sieveH.ValidateScript)

		// Contacts suggest (autocomplete)
		r.Get("/api/v1/accounts/{id}/contacts/suggest", contactH.SuggestContacts)

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
		r.Get("/api/v1/admin/pipelines/logs", pipelineH.ListPipelineLogs)

		// Custom filters
		r.Get("/api/v1/admin/custom-filters", pipelineH.ListCustomFilters)
		r.Post("/api/v1/admin/custom-filters", pipelineH.CreateCustomFilter)
		r.Delete("/api/v1/admin/custom-filters/{id}", pipelineH.DeleteCustomFilter)
		r.Post("/api/v1/admin/custom-filters/validate", pipelineH.ValidateCustomFilter)

		// Queue management
		r.Get("/api/v1/admin/queue", queueH.ListQueue)
		r.Get("/api/v1/admin/queue/stats", queueH.QueueStats)
		r.Post("/api/v1/admin/queue/bulk-retry", queueH.BulkRetry)
		r.Post("/api/v1/admin/queue/bulk-bounce", queueH.BulkBounce)
		r.Delete("/api/v1/admin/queue/bulk-delete", queueH.BulkDelete)
		r.Get("/api/v1/admin/queue/{id}", queueH.GetQueueEntry)
		r.Post("/api/v1/admin/queue/{id}/retry", queueH.RetryQueueEntry)
		r.Post("/api/v1/admin/queue/{id}/bounce", queueH.BounceQueueEntry)
		r.Delete("/api/v1/admin/queue/{id}", queueH.DeleteQueueEntry)

		// Sender allowlist/blocklist
		r.Get("/api/v1/admin/domains/{id}/allowlist", senderRuleH.ListAllowlist)
		r.Post("/api/v1/admin/domains/{id}/allowlist", senderRuleH.AddToAllowlist)
		r.Delete("/api/v1/admin/domains/{id}/allowlist/{eid}", senderRuleH.RemoveFromAllowlist)
		r.Get("/api/v1/admin/domains/{id}/blocklist", senderRuleH.ListBlocklist)
		r.Post("/api/v1/admin/domains/{id}/blocklist", senderRuleH.AddToBlocklist)
		r.Delete("/api/v1/admin/domains/{id}/blocklist/{eid}", senderRuleH.RemoveFromBlocklist)

		// DKIM key management
		r.Get("/api/v1/admin/dkim", dkimH.ListKeys)
		r.Put("/api/v1/admin/dkim/{id}", dkimH.SetKey)
		r.Delete("/api/v1/admin/dkim/{id}", dkimH.DeleteKey)

		// Certificate management
		r.Get("/api/v1/admin/certificates", certH.ListCertificates)
		r.Get("/api/v1/admin/certificates/{id}", certH.GetCertificate)
		r.Post("/api/v1/admin/certificates", certH.CreateCertificate)
		r.Delete("/api/v1/admin/certificates/{id}", certH.DeleteCertificate)

		// Ban management
		r.Get("/api/v1/admin/bans", banH.ListBans)
		r.Post("/api/v1/admin/bans", banH.CreateBan)
		r.Delete("/api/v1/admin/bans/{id}", banH.DeleteBan)
		r.Delete("/api/v1/admin/bans/ip/{ip}", banH.UnbanIP)

		// Logs
		r.Get("/api/v1/admin/logs/delivery", logH.DeliveryLog)
		r.Get("/api/v1/admin/logs/activity", logH.ActivityLog)

		// Test endpoints (non-production only)
		r.Post("/api/v1/admin/test/send", testH.SendTestEmail)
		r.Get("/api/v1/admin/test/verify", testH.VerifyDelivery)
		r.Post("/api/v1/admin/test/probe", testH.ProbeServices)
		r.Post("/api/v1/admin/test/reset", testH.ResetTestData)
		r.Post("/api/v1/admin/test/seed", testH.SeedTestData)
		r.Post("/api/v1/admin/test/snapshot", testH.Snapshot)
		r.Post("/api/v1/admin/test/snapshot/restore", testH.RestoreSnapshot)
	})

	return r
}
