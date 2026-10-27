import { useState, type FormEvent } from "react";
import { apiV1 } from "../../lib/api";
import { expectSuccess, type JsonResponse } from "../../lib/api-contract";

function webmailAddress(value: string): URL {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error("Enter the full HTTPS address of your webmail.");
  }
  const local = ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
  if (
    (url.protocol !== "https:" && !(url.protocol === "http:" && local)) ||
    url.username ||
    url.password
  ) {
    throw new Error(
      "Use an HTTPS webmail address without embedded credentials.",
    );
  }
  url.search = "";
  url.hash = "";
  return url;
}

// A fragment is kept out of HTTP requests, access logs and referrer headers.
export function invitationLink(base: string, token: string): string {
  const url = webmailAddress(base);
  if (!token) throw new Error("The server did not return an invitation token.");
  url.hash = new URLSearchParams({ invite_token: token }).toString();
  return url.toString();
}

export function InvitationCard({
  accountId,
  active,
}: {
  accountId: number;
  active: boolean;
}) {
  const [webmail, setWebmail] = useState("");
  const [link, setLink] = useState("");
  const [expires, setExpires] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");

  async function issue(event: FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    setCopied(false);
    try {
      const base = webmailAddress(webmail).toString();
      // Hide an older link while reissuing; the server invalidates it atomically.
      setLink("");
      setExpires("");
      const response = await apiV1.request(
        `/admin/mailboxes/${accountId}/invitation`,
        { method: "POST" },
      );
      await expectSuccess(response, "Could not create an invitation");
      const result: JsonResponse<"inviteAccount", 201> = await response.json();
      setLink(invitationLink(base, result.data.token));
      setExpires(result.data.expires_at);
    } catch (failure) {
      setError(
        failure instanceof Error
          ? failure.message
          : "Could not create an invitation",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <section
      className="p-6 border space-y-4"
      style={{
        borderColor: "var(--gray-border)",
        backgroundColor: "var(--bg-surface)",
      }}
    >
      <h2 className="text-lg font-semibold">Invite the account owner</h2>
      <p className="text-sm">
        For an account created without credentials. The recipient chooses their
        password and sets up an authenticator with recovery codes. Invitations
        expire after 15 minutes; creating another cancels the previous one.
      </p>
      <form onSubmit={issue} className="space-y-3">
        <label className="block text-sm">
          Webmail address
          <input
            type="url"
            required
            value={webmail}
            onChange={(event) => setWebmail(event.target.value)}
            placeholder="https://mail.example.com"
            className="mt-1 border p-3 w-full"
          />
        </label>
        <button
          disabled={busy || !active}
          className="border px-4 py-2 disabled:opacity-50"
        >
          {busy ? "Creating…" : "Create invitation"}
        </button>
      </form>
      {!active && (
        <p className="text-sm">Enable the account before inviting its owner.</p>
      )}
      {error && <p role="alert">{error}</p>}
      {link && (
        <div className="space-y-3">
          <label className="block text-sm">
            Private invitation link
            <input
              readOnly
              value={link}
              onFocus={(event) => event.target.select()}
              className="mt-1 border p-3 w-full"
            />
          </label>
          <p className="text-sm">
            Share this privately. Expires {new Date(expires).toLocaleString()}.
          </p>
          <button
            className="border px-4 py-2"
            onClick={() => {
              void navigator.clipboard
                .writeText(link)
                .then(() => setCopied(true))
                .catch(() =>
                  setError("Copy the private link from the field above."),
                );
            }}
          >
            Copy invitation link
          </button>
          {copied && <p role="status">Invitation link copied.</p>}
        </div>
      )}
    </section>
  );
}
