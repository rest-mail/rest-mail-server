package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// An operator stops one domain's outbound mail when it is hurting the server's
// reputation (#1590). While stopped, what the domain's mailboxes send is accepted
// and queued as ever, but the outbound worker holds it, along with the mail it
// had already queued: none of it is delivered, bounced or dropped. Mail to the
// domain is delivered as before. Restarting releases the held mail for normal
// delivery, with the time it was held added back to its queue lifetime.
//
// Both are the server operator's: they need queue:manage at "*", the server's
// own scope, so a domain's administrators cannot lift a stop put on their domain.

const outboundStopDescription = "Stops the domain's outbound mail, with the reason the operator gives. Mail its mailboxes send is still accepted and queued, but held, as is the mail it already has queued: none of it is delivered, bounced or lost until the stop is lifted. Mail to the domain is delivered as before. Stopping a stopped domain replaces the reason. The stop, who made it, when and why, is written to the audit log, and the domain answers with it in outbound_stop."

const outboundRestartDescription = "Lifts the domain's outbound stop: the mail held while it was stopped is released for normal delivery, its queue lifetime extended by the time it was held. The restart, who made it and when, is written to the audit log. Restarting a domain that is not stopped changes nothing and writes nothing."

func (h *DomainHandler) registerOutboundStop(rs endpoint.Routes, checks DomainRouteChecks) {
	tags := []string{"Admin: Domains"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/admin/domains/{id}/outbound-stop",
		OperationID: "stopDomainOutbound", Summary: "Stop a domain's outbound mail", Description: outboundStopDescription,
		Tags: tags, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueManage)}}, h.stopOutbound)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/domains/{id}/outbound-stop",
		OperationID: "restartDomainOutbound", Summary: "Restart a domain's outbound mail", Description: outboundRestartDescription,
		Tags: tags, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueManage)}}, h.restartOutbound)
}

type stopOutboundRequest struct {
	ID   uint32           `path:"id" minimum:"1" example:"1"`
	Body stopOutboundBody `body:""`
}

type stopOutboundBody struct {
	// Reason is why the domain's outbound mail is stopped, shown with the stop and
	// kept in the audit log.
	Reason string `json:"reason" minLength:"1" maxLength:"1000" pattern:"\\S" message:"required" missing:"required"`
}

func outboundStopOf(d models.Domain) *apitypes.DomainOutboundStop {
	if d.OutboundStoppedAt == nil {
		return nil
	}
	return &apitypes.DomainOutboundStop{StoppedAt: *d.OutboundStoppedAt, StoppedBy: d.OutboundStoppedBy, Reason: d.OutboundStopReason}
}

func outboundAuditEntry(r *http.Request, action string, d models.Domain) models.AuditEntry {
	e := middleware.RequestEntry(r, action, http.StatusOK)
	e.Target["domain_id"] = strconv.FormatUint(uint64(d.ID), 10)
	e.Target["domain"] = d.Name
	return e
}

func (h *DomainHandler) stopOutbound(r *http.Request, in stopOutboundRequest) (Domain, error) {
	reason := strings.TrimSpace(in.Body.Reason)
	var domain models.Domain
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := lockDomain(tx, in.ID, &domain); err != nil {
			return err
		}
		by := ""
		if c := middleware.GetClaims(r); c != nil {
			by = c.Address
		}
		now := time.Now().UTC()
		updates := map[string]any{"outbound_stop_reason": reason, "outbound_stopped_by": by}
		if domain.OutboundStoppedAt == nil {
			updates["outbound_stopped_at"] = now
		}
		if err := tx.Model(&domain).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.First(&domain, domain.ID).Error; err != nil {
			return err
		}
		e := outboundAuditEntry(r, "domain.outbound_stop", domain)
		e.Target["reason"] = reason
		return audit.Append(tx, &e)
	})
	if err != nil {
		return Domain{}, err
	}
	return domainOf(domain), nil
}

func (h *DomainHandler) restartOutbound(r *http.Request, in domainPath) (Domain, error) {
	var domain models.Domain
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := lockDomain(tx, in.ID, &domain); err != nil {
			return err
		}
		if domain.OutboundStoppedAt == nil {
			return nil
		}
		stoppedAt, reason := *domain.OutboundStoppedAt, domain.OutboundStopReason
		held := time.Since(stoppedAt)
		if held < 0 {
			held = 0
		}
		// The hold must not spend the mail's queue lifetime: give each message back
		// the time it was held (the stop's age, or its own if it was queued since),
		// so it is retried for as long as if it had never been stopped.
		if err := tx.Model(&models.OutboundQueue{}).
			Where("status IN ('pending', 'deferred', 'delivering') AND "+models.OutboundQueueSendingDomainSQL+" = ?", domain.Name).
			Update("expires_at", gorm.Expr("expires_at + LEAST(make_interval(secs => ?), GREATEST(now() - created_at, interval '0'))", held.Seconds())).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain).Updates(map[string]any{"outbound_stopped_at": nil, "outbound_stop_reason": "", "outbound_stopped_by": ""}).Error; err != nil {
			return err
		}
		e := outboundAuditEntry(r, "domain.outbound_restart", domain)
		e.Target["stopped_at"] = stoppedAt.UTC().Format(time.RFC3339)
		e.Target["reason"] = reason
		if err := audit.Append(tx, &e); err != nil {
			return err
		}
		return tx.First(&domain, domain.ID).Error
	})
	if err != nil {
		return Domain{}, err
	}
	return domainOf(domain), nil
}

// lockDomain reads the domain the path names, locked for the transaction.
func lockDomain(tx *gorm.DB, id uint32, domain *models.Domain) error {
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(domain, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errDomainNotFound
	}
	return err
}
