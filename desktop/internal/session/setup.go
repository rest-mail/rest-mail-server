package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// The first-run steps the server names in setup_required.
const (
	SetupPassword  = "password"
	SetupTwoFactor = "two_factor"
)

// setupTimeout bounds each first-run request, which the user waits on.
const setupTimeout = 30 * time.Second

// ChangePassword replaces the signed-in account's password. The server ends
// every session of the account when it succeeds, this one included, so the
// manager signs in again with the new password. An account that already has
// an authenticator then needs SubmitTOTP, as after SignIn.
func (manager *Manager) ChangePassword(current, replacement string) (SignInResult, error) {
	defer manager.notify()
	if current == "" || replacement == "" {
		return SignInResult{}, errors.New("current and new passwords are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	if err := manager.post(ctx, "/api/v1/auth/password", map[string]string{"current_password": current, "new_password": replacement}, nil); err != nil {
		return SignInResult{}, err
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	login := pendingLogin{server: manager.current.Server, email: manager.current.Email, password: replacement, expires: time.Now().Add(5 * time.Minute)}
	manager.access = ""
	err := manager.loginLocked(login, "")
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.Code == "totp_required" {
		manager.pending = login
		return SignInResult{NeedsTOTP: true}, nil
	}
	if err != nil {
		_ = manager.clearLocked()
		return SignInResult{}, errors.New("password changed; sign in again with the new password")
	}
	return SignInResult{SetupRequired: slices.Clone(manager.setup)}, nil
}

// EnrollTwoFactor starts an authenticator enrollment and returns what the user
// must see to add it. Nothing is switched on until ConfirmTwoFactor succeeds.
func (manager *Manager) EnrollTwoFactor() (TwoFactorEnrollment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	var enrollment struct {
		Secret        string   `json:"secret"`
		OTPAuthURL    string   `json:"otpauth_url"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := manager.post(ctx, "/api/v1/auth/2fa/enroll", map[string]string{}, &enrollment); err != nil {
		return TwoFactorEnrollment{}, err
	}
	if enrollment.Secret == "" || !strings.HasPrefix(enrollment.OTPAuthURL, "otpauth://") {
		return TwoFactorEnrollment{}, errors.New("server returned an incomplete authenticator enrollment")
	}
	return TwoFactorEnrollment{Secret: enrollment.Secret, OTPAuthURL: enrollment.OTPAuthURL, RecoveryCodes: enrollment.RecoveryCodes}, nil
}

// ConfirmTwoFactor switches the pending authenticator on with its first code,
// then refreshes the session so its setup state comes from the server again.
func (manager *Manager) ConfirmTwoFactor(code string) error {
	defer manager.notify()
	if strings.TrimSpace(code) == "" {
		return errors.New("a two-factor code is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	if err := manager.post(ctx, "/api/v1/auth/2fa/confirm", map[string]string{"totp_code": strings.TrimSpace(code)}, nil); err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := manager.refreshLocked(ctx); err != nil {
		return errors.New("authenticator added; sign in again to continue")
	}
	return nil
}

// post sends one authenticated JSON request and decodes the data envelope
// into result when result is not nil. A refusal comes back as an error a
// person can read, built from the server's message and field problems.
func (manager *Manager) post(ctx context.Context, path string, body any, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return errors.New("could not encode request")
	}
	response, err := manager.Do(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return errors.New("mail server unavailable")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return refusal(response.StatusCode, payload)
	}
	if result == nil {
		return nil
	}
	envelope := struct {
		Data any `json:"data"`
	}{Data: result}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return errors.New("invalid response from mail server")
	}
	return nil
}

func refusal(status int, payload []byte) error {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Fields map[string]string `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &body)
	message := body.Error.Message
	if len(body.Error.Details.Fields) != 0 {
		problems := make([]string, 0, len(body.Error.Details.Fields))
		for field, problem := range body.Error.Details.Fields {
			problems = append(problems, strings.ReplaceAll(field, "_", " ")+" "+problem)
		}
		sort.Strings(problems)
		message = strings.Join(problems, "; ")
		message = strings.ToUpper(message[:1]) + message[1:]
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &APIError{Status: status, Code: body.Error.Code, Message: message}
}
