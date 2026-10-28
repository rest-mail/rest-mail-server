package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
)

// Tokens stay in fragments and memory, never query strings, storage or history.
// A deliberate submit prevents link scanners from consuming verification links.
const recoveryEmailScript = `
let token = location.hash.slice(1);
const mode = new URLSearchParams(location.search).get('mode');
history.replaceState(null, '', location.pathname);
const form = document.getElementById('recovery');
const message = document.getElementById('message');
const password = document.getElementById('password');
const button = document.getElementById('submit');
if (!token || !['verify','recover'].includes(mode)) {
 message.textContent = 'Invalid link. Request another link.'; form.hidden = true;
} else {
 password.parentElement.hidden = mode !== 'recover';
 password.required = mode === 'recover';
 button.textContent = mode === 'verify' ? 'Verify recovery email' : 'Change password';
 form.addEventListener('submit', async event => {
  event.preventDefault(); button.disabled = true;
  const body = { token };
  if (mode === 'recover') body.new_password = password.value;
  try {
   const response = await fetch(mode === 'verify' ? '/api/v1/auth/recovery-email/verify' : '/api/v1/auth/recover/email/redeem', {
    method: 'POST', credentials: 'omit', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body)
   });
   const result = await response.json();
   message.textContent = response.ok ? result.data.message : result.error.message;
   if (response.ok) { form.hidden = true; password.value = ''; token = ''; } else { button.disabled = false; }
  } catch (_) { message.textContent = 'Request failed. Please try again.'; button.disabled = false; }
 });
}
`

func (h *RecoveryEmailHandler) linkPage(_ *http.Request, _ struct{}) (endpoint.Document, error) {
	sum := sha256.Sum256([]byte(recoveryEmailScript))
	policy := "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
	return endpoint.Document{ContentType: "text/html; charset=utf-8", Header: http.Header{
		"Cache-Control": {"no-store"}, "Referrer-Policy": {"no-referrer"}, "Content-Security-Policy": {policy},
	}, Body: []byte(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>REST Mail account recovery</title><main><h1>Account recovery</h1><p id="message" role="status">Confirm the action below to use your private link.</p><form id="recovery"><label>New password <input id="password" type="password" autocomplete="new-password" minlength="12" maxlength="72"></label><button id="submit" type="submit">Continue</button></form><p>After changing your password, sign in through your usual mail app. Your existing second factor still applies.</p></main><script>` + recoveryEmailScript + `</script></html>`)}, nil
}
