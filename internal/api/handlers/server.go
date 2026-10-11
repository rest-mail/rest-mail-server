package handlers

import (
	"net/http"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/version"
	"gorm.io/gorm"
)

// ServerHandler answers GET /api/v1/server: what this server is, and whether
// anyone has claimed it, for anyone who can reach it.
//
// A client cannot look behind the server — it does not know, and must not
// need to know, how the server was installed — so the server describes itself.
// That is how `restmail status` and the app decide whether to offer claiming or
// signing in, rather than finding out by failing.
//
// Nothing here is a secret. Whether the server is claimed, and which ways of
// claiming it exist, were already discoverable from how /auth/claim and
// /auth/owner/challenge answer; this says so on purpose instead of by accident.
// The version is the one deliberate disclosure, so that what is running can be
// confirmed from outside.
type ServerHandler struct {
	db        *gorm.DB
	ownerKeys *bootstrap.OwnerKeys
}

func NewServerHandler(db *gorm.DB, ownerKeys *bootstrap.OwnerKeys) *ServerHandler {
	return &ServerHandler{db: db, ownerKeys: ownerKeys}
}

// serverDescription is the body of GET /api/v1/server.
type serverDescription struct {
	Product string `json:"product"`
	Version string `json:"version"`
	// Claimed is whether the server has an admin. Before it does, it can be
	// claimed; after, it is signed in to.
	Claimed bool `json:"claimed"`
	// ClaimMethods says how an unclaimed server can be claimed: "setup_code",
	// the code it prints to its own output at every start; and "owner_key",
	// when it lists owner keys. Empty once claimed.
	ClaimMethods []string `json:"claim_methods"`
}

func (h *ServerHandler) Describe(w http.ResponseWriter, _ *http.Request) {
	var admins int64
	if err := h.db.Model(&models.AdminUser{}).Count(&admins).Error; err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "unavailable", "The server cannot read its own state")
		return
	}
	description := serverDescription{
		Product:      "rest-mail",
		Version:      version.Version,
		Claimed:      admins > 0,
		ClaimMethods: []string{},
	}
	if !description.Claimed {
		// An unclaimed server always prints a setup code: one is minted at
		// every start while there is no admin (bootstrap.EnsureSetupCode).
		description.ClaimMethods = append(description.ClaimMethods, "setup_code")
		if h.ownerKeys.Len() > 0 {
			description.ClaimMethods = append(description.ClaimMethods, "owner_key")
		}
	}
	respond.Data(w, http.StatusOK, description)
}
