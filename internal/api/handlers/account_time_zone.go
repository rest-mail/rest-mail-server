package handlers

import (
	"fmt"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/timezone"
)

type setAccountTimeZoneRequest struct {
	ID   uint32                   `path:"id" minimum:"1" example:"1"`
	Body apitypes.AccountTimeZone `body:""`
}

func (h *ContactHandler) registerAccountTimeZone(rs endpoint.Routes) {
	schemes := []string{"CookieAuth", "BearerAuth", "APIKeyAuth", "GatewaySessionAuth"}
	tags := []string{"Accounts"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/time-zone",
		OperationID: "getAccountTimeZone", Summary: "Get the account's time zone", Tags: tags,
		Description: "The IANA time zone whose calendar days IMAP SEARCH ON, BEFORE and SINCE compare for this account. Null when none is set, and IMAP then uses UTC. Timestamps are stored and returned in UTC whatever this says.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: handlerChecked(models.CapMailRead, schemes...)}, h.getAccountTimeZone)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/accounts/{id}/time-zone",
		OperationID: "setAccountTimeZone", Summary: "Set the account's time zone", Tags: tags,
		Description: "Set the IANA time zone whose calendar days IMAP SEARCH ON, BEFORE and SINCE compare for this account, or null to clear it and use UTC. It applies to the next SEARCH, including on connections already open. An unknown name is refused. Stored timestamps do not change, and IMAP INTERNALDATE is still rendered in UTC.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: handlerChecked(models.CapMailWrite, schemes...)}, h.setAccountTimeZone)
}

func (h *ContactHandler) getAccountTimeZone(r *http.Request, in contactsPath) (apitypes.AccountTimeZone, error) {
	account, err := h.resolveContactAccount(r, in.ID, models.CapMailRead)
	if err != nil {
		return apitypes.AccountTimeZone{}, err
	}
	return apitypes.AccountTimeZone{TimeZone: account.TimeZone}, nil
}

func (h *ContactHandler) setAccountTimeZone(r *http.Request, in setAccountTimeZoneRequest) (apitypes.AccountTimeZone, error) {
	account, err := h.resolveContactAccount(r, in.ID, models.CapMailWrite)
	if err != nil {
		return apitypes.AccountTimeZone{}, err
	}
	if in.Body.TimeZone != nil {
		if _, err := timezone.Load(*in.Body.TimeZone); err != nil {
			return apitypes.AccountTimeZone{}, endpoint.Invalid(map[string]string{"time_zone": "must be an IANA time zone name"})
		}
	}
	result := h.db.WithContext(r.Context()).Model(account).Update("time_zone", in.Body.TimeZone)
	if result.Error != nil {
		return apitypes.AccountTimeZone{}, fmt.Errorf("save account time zone: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return apitypes.AccountTimeZone{}, errContactAccess
	}
	return in.Body, nil
}
