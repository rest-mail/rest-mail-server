# Unsubscribing from list mail

A received message whose `List-Unsubscribe` field (RFC 2369) offers a target can be
unsubscribed from in the webmail, with the Unsubscribe action on the message. Nothing is
sent until the person confirms, and the dialog names the method.

## Targets

`GET /api/v1/messages/{id}/unsubscribe` (mail:read) answers the targets. It sends nothing.

| Field | What it is |
| --- | --- |
| `one_click` | The first HTTPS URI, present only when `List-Unsubscribe-Post` is exactly `List-Unsubscribe=One-Click` and a DKIM signature covers both list fields (RFC 8058 §4, below). Otherwise that URI is the first of `links`. |
| `links` | The other HTTPS URIs: pages the person opens in a browser. The server never fetches them. |
| `mailto` | `mailto:` targets with their `subject` and `body` (RFC 6068). |

The field is written by the sender, so each item is checked and the ones that fail are
left out: an `http:` URI, an HTTPS URI with a user name or password, a `mailto:` URI with
more than one address or a line break in its subject, and anything else. A field given more
than once is not read, as message classes do not read it ([message-classes.md](message-classes.md)).
A `to` hfield in a `mailto:` URI is ignored, so a target cannot add recipients.

## One-click

`POST /api/v1/messages/{id}/unsubscribe/one-click` (mail:write) makes the server send one
`POST` with the body `List-Unsubscribe=One-Click` (RFC 8058 §3.1) to the `one_click` URI.
It is sent only on this request, never on delivery or on reading the message.

RFC 8058 §4 requires a signature: "The DKIM signature MUST cover the List-Unsubscribe and
List-Unsubscribe-Post header fields." So a target is one-click only when a DKIM signature
that this server verified as `pass` on delivery lists both fields in its `h=` tag (compared
without regard to case). The `Authentication-Results` field that delivery writes first
records one `dkim=` verdict per `DKIM-Signature` field, in order, with its `d=`, but not
which fields it signed. So the server pairs the verdicts with the stored `DKIM-Signature`
fields in order and reads each one's `h=` tag. If they cannot be paired one to one, or a
pair's `d=` differs, nothing counts as signed. Without such a signature the URI is listed
as an ordinary link, and the one-click route refuses it with `422
unsubscribe_target_refused`.

The server refuses with `422 unsubscribe_target_refused`, without sending, when:

- the URI is not HTTPS, or carries a user name or password;
- the host is, or resolves to, any loopback, private, link-local, unique-local, multicast,
  carrier-grade NAT, reserved or documentation address (every resolved address is
  checked);
- a redirect goes anywhere the first URI could not (each of at most three hops is checked);
- the address actually dialed is one of those, which catches a name that resolves
  differently at dial time (DNS rebinding).

No proxy is used, and no cookie or credential is sent. A target that cannot be reached, or
answers other than 2xx, is `502 unsubscribe_failed`.

## mailto

`POST /api/v1/messages/{id}/unsubscribe/mailto` with `{"index": 0}` sends the message the
target at `index` asks for, through `sendMessage`: sender authorization (mail:send, or the
delegated sending capabilities), the rate limits, the recipient checks and the outbound
pipeline all apply. `from` defaults to the message's account. An empty subject or body is
sent as `unsubscribe`.
