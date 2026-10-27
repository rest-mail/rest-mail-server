package handlers

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
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

// pushSession says whether a session can have devices: a session signed in
// with a password, not a credential sent with HTTP Basic. Every account is a
// mailbox, so every session has mail to be told about.
func pushSession(claims *auth.Claims) bool {
	return claims.CredentialID == 0
}

// List answers which push services the server can use for this session, and
// the session's devices. A session that cannot have devices is offered no
// service, which the app reads as "off", and has none.
// GET /api/v1/devices
func (h *DeviceHandler) list(r *http.Request, _ struct{}) (deviceListResponse, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return deviceListResponse{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	platforms := []string{}
	if pushSession(claims) {
		platforms = append(platforms, models.PushPlatformAPNs)
	}
	registered, err := h.store.WithContext(r.Context()).List(claims.SessionID)
	if err != nil {
		return deviceListResponse{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list devices")
	}
	devices := make([]deviceResponse, 0, len(registered))
	for i := range registered {
		devices = append(devices, deviceJSON(&registered[i]))
	}
	return deviceListResponse{Platforms: platforms, Devices: devices}, nil
}

type registerDeviceRequest struct {
	Platform    string `json:"platform"`
	Token       string `json:"token"`
	Environment string `json:"environment,omitempty"`
	AppID       string `json:"app_id,omitempty"`
}

// Register registers this phone with this session: 201 with the new device,
// or 200 with the one the session already registered for the token.
// POST /api/v1/devices
func (h *DeviceHandler) register(r *http.Request, req registerDeviceRequest) (endpoint.Status[deviceResponse], error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.Status[deviceResponse]{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
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
		return endpoint.Status[deviceResponse]{}, endpoint.Invalid(fields)
	}

	store := h.store.WithContext(r.Context())
	now := h.now()
	alive, err := store.SessionAlive(claims.SessionID, now)
	if err != nil {
		return endpoint.Status[deviceResponse]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to register the device")
	}
	if !alive {
		return endpoint.Status[deviceResponse]{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "This session has ended; sign in again")
	}
	device, created, err := store.Register(&models.PushDevice{
		SessionID:   claims.SessionID,
		MailboxID:   claims.AccountID,
		Platform:    req.Platform,
		Token:       req.Token,
		Environment: req.Environment,
	})
	if errors.Is(err, push.ErrTooManyDevices) {
		return endpoint.Status[deviceResponse]{}, endpoint.Invalid(map[string]string{
			"token": "this session has registered " + strconv.Itoa(push.MaxDevicesPerSession) + " devices, the most it may; remove one first",
		})
	}
	if err != nil {
		return endpoint.Status[deviceResponse]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to register the device")
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return endpoint.Status[deviceResponse]{Code: status, Value: deviceJSON(device)}, nil
}

// Remove stops pushes to a device this session registered: 204, or 404 when
// the id is not one of this session's.
// DELETE /api/v1/devices/{device_id}
func (h *DeviceHandler) remove(r *http.Request, req devicePath) (endpoint.Empty, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	id, err := strconv.ParseUint(req.ID, 10, 32)
	if err != nil || id == 0 {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Device not found")
	}
	ok, err := h.store.WithContext(r.Context()).Remove(claims.SessionID, uint(id))
	if err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to remove the device")
	}
	if !ok {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Device not found")
	}
	return endpoint.Empty{}, nil
}

// RegisterRoutes describes exactly the device routes this configured server serves.
func (h *DeviceHandler) RegisterRoutes(rs endpoint.Routes, audit func(string, ...middleware.AuditOption) endpoint.Middleware) {
	tags := []string{"Push"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/devices",
		OperationID: "listPushDevices", Summary: "List push services and this session's devices", Tags: tags,
		Description: "List lookup follows request cancellation; dependency failures return 500.",
		Errors:      []int{http.StatusUnauthorized}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/devices",
		OperationID: "registerPushDevice", Summary: "Register a push device for this session", Tags: tags,
		Description: "Session lookup and the registration transaction follow request cancellation; dependency failures return 500.",
		Status:      http.StatusCreated, SuccessStatuses: []int{http.StatusOK},
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
		Middleware: []endpoint.Middleware{audit(push.ActionDeviceRegister, middleware.AuditBodyFields("platform", "environment"),
			middleware.AuditResponseField("id", "device_id"))}}, h.register)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/devices/{device_id}",
		OperationID: "removePushDevice", Summary: "Remove a push device registered by this session", Tags: tags,
		Description: "Removal follows request cancellation; dependency failures return 500 and absent devices retain 404.",
		Status:      http.StatusNoContent, Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
		Middleware: []endpoint.Middleware{audit(push.ActionDeviceRemove)}}, h.remove)
}

type deviceListResponse struct {
	Platforms []string         `json:"platforms"`
	Devices   []deviceResponse `json:"devices"`
}

// Keep a malformed id's existing 404; the id is a string in device responses.
type devicePath struct {
	ID string `path:"device_id"`
}
