package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/push"
	"gorm.io/gorm"
)

// apnsTokenPattern is an APNs device token as the app sends it: lowercase
// hex, 32 bytes today, longer allowed.
var apnsTokenPattern = regexp.MustCompile(`^[0-9a-f]{64,200}$`)

// DeviceHandler serves /api/v1/devices: the phones a session registers for
// push (#363). The routes exist only when push is configured; a server
// without it answers 404 on all three, which the app reads as "off".
type DeviceHandler struct {
	store *push.Store
	// topic is the bundle id APNs pushes go to; a registration naming another
	// app is refused.
	topic string
	now   func() time.Time
}

// NewDeviceHandler serves the devices routes for APNs pushes to topic.
func NewDeviceHandler(db *gorm.DB, topic string) *DeviceHandler {
	return &DeviceHandler{store: push.NewStore(db), topic: topic, now: time.Now}
}

// deviceResponse is a device as the API shows it. Never the token.
type deviceResponse struct {
	ID          string    `json:"id"`
	Platform    string    `json:"platform"`
	Environment string    `json:"environment"`
	CreatedAt   time.Time `json:"created_at"`
}

func deviceJSON(d *models.PushDevice) deviceResponse {
	return deviceResponse{ID: push.DeviceID(d), Platform: d.Platform, Environment: d.Environment, CreatedAt: d.CreatedAt}
}

// pushSession says whether a session can have devices: a mailbox's, signed
// in with a password. An admin has no mailbox to be told about.
func pushSession(claims *auth.Claims) bool {
	return claims.UserType == "mailbox" && claims.CredentialID == 0
}

// List answers which push services the server can use for this session, and
// the session's devices. A session that cannot have devices is offered no
// service, which the app reads as "off", and has none.
// GET /api/v1/devices
func (h *DeviceHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	platforms := []string{}
	if pushSession(claims) {
		platforms = append(platforms, models.PushPlatformAPNs)
	}
	registered, err := h.store.List(claims.SessionID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list devices")
		return
	}
	devices := make([]deviceResponse, 0, len(registered))
	for i := range registered {
		devices = append(devices, deviceJSON(&registered[i]))
	}
	respond.Data(w, http.StatusOK, map[string]any{"platforms": platforms, "devices": devices})
}

type registerDeviceRequest struct {
	Platform    string `json:"platform"`
	Token       string `json:"token"`
	Environment string `json:"environment"`
	AppID       string `json:"app_id"`
}

// Register registers this phone with this session: 201 with the new device,
// or 200 with the one the session already registered for the token.
// POST /api/v1/devices
func (h *DeviceHandler) Register(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	var req registerDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	fields := map[string]string{}
	switch {
	case !pushSession(claims):
		fields["platform"] = "an administrator's session has no mailbox to be told about"
	case req.Platform != models.PushPlatformAPNs:
		fields["platform"] = "not configured on this server"
	}
	if req.Platform == models.PushPlatformAPNs && !apnsTokenPattern.MatchString(req.Token) {
		fields["token"] = "not an APNs device token: 64 or more lowercase hex digits"
	}
	if req.Environment == "" {
		req.Environment = models.APNsProduction
	}
	if req.Environment != models.APNsProduction && req.Environment != models.APNsSandbox {
		fields["environment"] = `must be "production" or "sandbox"`
	}
	if req.AppID != "" && req.AppID != h.topic {
		fields["app_id"] = "this server pushes to " + h.topic + " only"
	}
	if len(fields) > 0 {
		respond.ValidationError(w, fields)
		return
	}

	now := h.now()
	alive, err := h.store.SessionAlive(claims.SessionID, now)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to register the device")
		return
	}
	if !alive {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "This session has ended; sign in again")
		return
	}
	device, created, err := h.store.Register(&models.PushDevice{
		SessionID:        claims.SessionID,
		MailboxID:        claims.MailboxID,
		WebmailAccountID: claims.WebmailAccountID,
		Platform:         req.Platform,
		Token:            req.Token,
		Environment:      req.Environment,
	})
	if errors.Is(err, push.ErrTooManyDevices) {
		respond.ValidationError(w, map[string]string{
			"token": "this session has registered " + strconv.Itoa(push.MaxDevicesPerSession) + " devices, the most it may; remove one first",
		})
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to register the device")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	respond.Data(w, status, deviceJSON(device))
}

// Remove stops pushes to a device this session registered: 204, or 404 when
// the id is not one of this session's.
// DELETE /api/v1/devices/{device_id}
func (h *DeviceHandler) Remove(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(r, "device_id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Device not found")
		return
	}
	ok, err := h.store.Remove(claims.SessionID, uint(id))
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to remove the device")
		return
	}
	if !ok {
		respond.Error(w, http.StatusNotFound, "not_found", "Device not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
