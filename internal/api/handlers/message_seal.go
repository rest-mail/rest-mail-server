package handlers

import (
	"fmt"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
)

type MessageSeal struct {
	State        string `json:"state" enums:"intact,mismatch,not_sealed" description:"intact means unchanged since server acceptance, not authentic or safe; mismatch means content changed; not_sealed means no receipt seal exists"`
	Algorithm    string `json:"algorithm" enums:"restmail-seal-v1"`
	SealedHash   string `json:"sealed_hash,omitempty" description:"SHA-256 hex stored once at acceptance; absent when not sealed"`
	ComputedHash string `json:"computed_hash,omitempty" description:"SHA-256 hex computed from the current stored source and content; absent when not sealed"`
}

func (handler *MessageHandler) verifyMessageSeal(request *http.Request, input messagePath) (MessageSeal, error) {
	message, err := handler.ownedMessage(request, input.ID, models.CapMailRead)
	if err != nil {
		return MessageSeal{}, err
	}
	result := MessageSeal{State: "not_sealed", Algorithm: "restmail-seal-v1", SealedHash: message.SealedHash}
	if message.SealedHash == "" {
		return result, nil
	}
	result.State = "mismatch"
	if message.SourceRef != nil {
		if handler.messageSources == nil {
			return MessageSeal{}, fmt.Errorf("verify message seal: %w", messagestore.ErrSourceUnavailable)
		}
		result.ComputedHash, err = handler.messageSources.SealDigest(request.Context(), message)
	} else {
		result.ComputedHash, err = message.SealDigest()
	}
	if err != nil {
		return MessageSeal{}, fmt.Errorf("verify message seal: %w", err)
	}
	if result.ComputedHash == result.SealedHash {
		result.State = "intact"
	}
	return result, nil
}
