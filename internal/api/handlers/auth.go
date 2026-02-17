package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type AuthHandler struct {
	db         *gorm.DB
	jwtService *auth.JWTService
}

func NewAuthHandler(db *gorm.DB, jwtService *auth.JWTService) *AuthHandler {
	return &AuthHandler{db: db, jwtService: jwtService}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken string   `json:"access_token"`
	ExpiresIn   int      `json:"expires_in"`
	User        userInfo `json:"user"`
}

type userInfo struct {
	ID          uint   `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		respond.ValidationError(w, map[string]string{
			"email":    "required",
			"password": "required",
		})
		return
	}

	// Find the mailbox
	var mailbox models.Mailbox
	if err := h.db.Where("address = ? AND active = ?", req.Email, true).First(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid email or password")
		return
	}

	// Check password
	if err := auth.CheckPassword(req.Password, mailbox.Password); err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid email or password")
		return
	}

	// Find or create webmail account
	var account models.WebmailAccount
	if err := h.db.Where("primary_mailbox_id = ?", mailbox.ID).First(&account).Error; err != nil {
		account = models.WebmailAccount{PrimaryMailboxID: mailbox.ID}
		if err := h.db.Create(&account).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create account")
			return
		}
	}

	// Generate tokens
	tokens, err := h.jwtService.GenerateTokenPair(mailbox.ID, mailbox.Address, account.ID, account.IsAdmin)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to generate tokens")
		return
	}

	// Set refresh token as HTTP-only cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "restmail_refresh",
		Value:    tokens.RefreshToken,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 60 * 60,
	})

	// Update last login
	h.db.Model(&mailbox).Update("last_login_at", time.Now())

	respond.Data(w, http.StatusOK, loginResponse{
		AccessToken: tokens.AccessToken,
		ExpiresIn:   tokens.ExpiresIn,
		User: userInfo{
			ID:          account.ID,
			Email:       mailbox.Address,
			DisplayName: mailbox.DisplayName,
		},
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "restmail_refresh",
		Value:    "",
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("restmail_refresh")
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "No refresh token")
		return
	}

	claims, err := h.jwtService.ValidateToken(cookie.Value)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired refresh token")
		return
	}

	tokens, err := h.jwtService.GenerateTokenPair(claims.MailboxID, claims.Email, claims.WebmailAccountID, claims.IsAdmin)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to generate tokens")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "restmail_refresh",
		Value:    tokens.RefreshToken,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 60 * 60,
	})

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"access_token": tokens.AccessToken,
		"expires_in":   tokens.ExpiresIn,
	})
}
