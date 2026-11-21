package filters

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

const (
	rspamdResponseBytes  = 1 << 20
	clamavResponseBytes  = 64 << 10
	webhookResponseBytes = 64 << 10
	sidecarResponseBytes = 33 << 20
)

func readFilterResponse(resp *http.Response, limit int64) ([]byte, error) {
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("filter response exceeds %d bytes", limit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) == limit {
		var extra [1]byte
		if _, err := io.ReadFull(resp.Body, extra[:]); err == nil {
			return nil, fmt.Errorf("filter response exceeds %d bytes", limit)
		} else if err != io.EOF {
			return nil, err
		}
	}
	return body, nil
}

func ReadSidecarResponse(resp *http.Response) ([]byte, error) {
	return readFilterResponse(resp, sidecarResponseBytes)
}

func filterHTTPTimeout(milliseconds int, fallback, headroom time.Duration) time.Duration {
	maxBudget := pipeline.DefaultFilterTimeout - headroom
	if milliseconds <= 0 {
		return fallback
	}
	if milliseconds >= int(maxBudget/time.Millisecond) {
		return maxBudget
	}
	return time.Duration(milliseconds) * time.Millisecond
}
