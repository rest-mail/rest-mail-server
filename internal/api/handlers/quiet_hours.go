package handlers

import (
	"fmt"
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/quiethours"
)

type setQuietHoursRequest struct {
	ID   uint32         `path:"id" minimum:"1" example:"1"`
	Body quietHoursBody `body:""`
}

type quietHoursBody quiethours.Setting

func (quietHoursBody) AnyOf() oapifly.AnyOfRule {
	return oapifly.AnyOfRule{Alternatives: []oapifly.Alternative{
		{Fields: map[string]string{"enabled": `enums:"false"`}},
		{Required: []string{"start", "end", "time_zone"}, Fields: map[string]string{
			"enabled": `enums:"true"`, "start": `minLength:"5"`, "end": `minLength:"5"`, "time_zone": `minLength:"1"`,
		}, Message: "required when enabled"},
	}}
}

func (h *ContactHandler) registerQuietHours(rs endpoint.Routes) {
	schemes := []string{"CookieAuth", "BearerAuth", "APIKeyAuth", "BasicAuth"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/quiet-hours",
		OperationID: "getQuietHours", Summary: "Get the account's notification quiet hours", Tags: []string{"Notifications"},
		Description: "Off by default. Local HH:MM start (inclusive) and end (exclusive), with an IANA time zone. A start later than end crosses midnight. Mail delivery and reading are unaffected.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: handlerChecked(models.CapMailRead, schemes...)}, h.getQuietHours)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/accounts/{id}/quiet-hours",
		OperationID: "setQuietHours", Summary: "Set the account's notification quiet hours", Tags: []string{"Notifications"},
		Description: "Hold push notifications during this daily local-time range, then send one account summary containing arrival and unread counts only. Nothing is sent if nothing arrived. Start and end must differ; enabled schedules require both times and an IANA time zone. Empty times and zone are allowed only while disabled. Skipped local times end at the first real instant outside the range; repeated local times are quiet whenever the local clock is inside the range. Mail still arrives and is readable normally.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: handlerChecked(models.CapMailWrite, schemes...)}, h.setQuietHours)
}

func (h *ContactHandler) getQuietHours(r *http.Request, in contactsPath) (quiethours.Setting, error) {
	account, err := h.resolveContactAccount(r, in.ID, models.CapMailRead)
	if err != nil {
		return quiethours.Setting{}, err
	}
	return quiethours.Setting{Enabled: account.QuietHoursEnabled, Start: account.QuietHoursStart, End: account.QuietHoursEnd, TimeZone: account.QuietHoursTimeZone}, nil
}

func (h *ContactHandler) setQuietHours(r *http.Request, in setQuietHoursRequest) (quiethours.Setting, error) {
	account, err := h.resolveContactAccount(r, in.ID, models.CapMailWrite)
	if err != nil {
		return quiethours.Setting{}, err
	}
	if fields := quiethours.Setting(in.Body).Validate(); len(fields) != 0 {
		return quiethours.Setting{}, endpoint.Invalid(fields)
	}
	result := h.db.WithContext(r.Context()).Model(account).Updates(map[string]any{
		"quiet_hours_enabled": in.Body.Enabled, "quiet_hours_start": in.Body.Start,
		"quiet_hours_end": in.Body.End, "quiet_hours_time_zone": in.Body.TimeZone,
	})
	if result.Error != nil {
		return quiethours.Setting{}, fmt.Errorf("save quiet hours: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return quiethours.Setting{}, errContactAccess
	}
	return quiethours.Setting(in.Body), nil
}
