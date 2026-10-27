import { useEffect, useState, type ReactNode } from "react";
import { AuthenticatorSetup } from "./AuthenticatorSetup";
import type { components } from "../../lib/api-types";
import type { JsonResponse, RequestBody } from "../../lib/api-contract";

export type SetupSession = Pick<
  JsonResponse<"login", 200>["data"],
  "user" | "capabilities" | "setup_required"
>;
export type SetupStep = NonNullable<SetupSession["setup_required"]>[number];
type Enrollment = JsonResponse<"enrollTwoFactor", 200>["data"];

interface Props {
  children: ReactNode;
  apiBase: string;
  email?: string;
  steps: SetupStep[];
  onSession: (session: SetupSession) => void;
  onSignOut: () => void;
}

type Recovery =
  | { kind: "token" | "invitation"; token: string }
  | { kind: "self" };

// A recovery token belongs in the fragment, never the query string sent to
// nginx/access logs. Remove it from browser history immediately; keep it only
// in this component until redeemed or cancelled. No local/session storage.
function readRecovery(): Recovery | null {
  const hash = new URLSearchParams(window.location.hash.slice(1));
  if (hash.has("invite") || hash.has("invite_token"))
    return { kind: "invitation", token: hash.get("invite_token") || "" };
  if (hash.has("reset") || hash.has("reset_token"))
    return { kind: "token", token: hash.get("reset_token") || "" };
  return hash.has("recover") ? { kind: "self" } : null;
}

export function AccountAccess({
  children,
  apiBase,
  email,
  steps,
  onSession,
  onSignOut,
}: Props) {
  const [recovery, setRecovery] = useState<Recovery | null>(readRecovery);
  const [recoveryEmail, setRecoveryEmail] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [currentPassword, setCurrentPassword] = useState("");
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [code, setCode] = useState("");
  const [savedCodes, setSavedCodes] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    const readHash = () => {
      const token = readRecovery();
      if (token !== null) {
        window.history.replaceState(
          null,
          "",
          window.location.pathname + window.location.search,
        );
        setRecovery(token);
        setError("");
        setNotice("");
        setRecoveryEmail("");
        setRecoveryCode("");
        setCode("");
        setPassword("");
        setConfirmPassword("");
      }
    };
    readHash();
    window.addEventListener("hashchange", readHash);
    return () => window.removeEventListener("hashchange", readHash);
  }, []);

  async function post(
    path: "/auth/recover",
    body: RequestBody<"recoverAccount">,
  ): Promise<SetupSession>;
  async function post(
    path: "/auth/reset" | "/auth/invitation",
    body: RequestBody<"resetAdmin">,
  ): Promise<SetupSession>;
  async function post(path: "/auth/2fa/enroll"): Promise<Enrollment>;
  async function post(
    path: "/auth/2fa/confirm",
    body: RequestBody<"confirmTwoFactor">,
  ): Promise<void>;
  async function post(
    path: "/auth/password",
    body: RequestBody<"changeOwnPassword">,
  ): Promise<void>;
  async function post(
    path: string,
    body?: unknown,
  ): Promise<SetupSession | Enrollment | void> {
    const csrf = document.cookie.match(/(?:^|;\s*)restmail_csrf=([^;]*)/);
    const response = await fetch(`${apiBase}${path}`, {
      method: "POST",
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf ? decodeURIComponent(csrf[1]) : "",
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) {
      const result: components["schemas"]["ValidationErrorResponse"] =
        await response.json();
      const details = Object.values(result.error?.details || {}).join("; ");
      throw new Error(
        details || result.error?.message || "Request failed. Please try again.",
      );
    }
    if (response.status === 204) return;
    const result:
      | JsonResponse<"recoverAccount", 200>
      | JsonResponse<"resetAdmin", 200>
      | JsonResponse<"enrollTwoFactor", 200> = await response.json();
    return result.data;
  }

  async function run(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : "Request failed. Please try again.",
      );
    } finally {
      setBusy(false);
    }
  }

  async function savePassword(event: React.FormEvent) {
    event.preventDefault();
    if (password !== confirmPassword) {
      setError("The new passwords do not match.");
      return;
    }
    if (new TextEncoder().encode(password).length < 12) {
      setError("Use at least 12 characters for your password.");
      return;
    }
    await run(async () => {
      if (recovery !== null) {
        const session =
          recovery.kind === "self"
            ? await post("/auth/recover", {
                email: recoveryEmail.trim(),
                totp_code: code.trim(),
                recovery_code: recoveryCode.trim(),
                new_password: password,
              })
            : await post(
                recovery.kind === "invitation"
                  ? "/auth/invitation"
                  : "/auth/reset",
                {
                  token: recovery.token.trim(),
                  password,
                },
              );
        onSession(session);
        setRecovery(null);
      } else {
        await post("/auth/password", {
          current_password: currentPassword,
          new_password: password,
        });
        onSignOut();
        setNotice(
          "Password changed. Sign in with your new password to finish setting up your authenticator.",
        );
      }
      setPassword("");
      setConfirmPassword("");
      setCurrentPassword("");
      setRecoveryEmail("");
      setRecoveryCode("");
      setEnrollment(null);
      setCode("");
      setSavedCodes(false);
    });
  }

  const resetting = recovery !== null;
  const selfRecovering = recovery?.kind === "self";
  const invited = recovery?.kind === "invitation";
  const changingPassword = steps.includes("password");
  const enrolling = !changingPassword && steps.includes("two_factor");
  if (!resetting && !changingPassword && !enrolling) {
    return (
      <>
        {notice && (
          <p
            role="status"
            className="p-4 text-center bg-green-50 text-green-900"
          >
            {notice}
          </p>
        )}
        {children}
      </>
    );
  }

  return (
    <main className="min-h-screen bg-white text-gray-900 flex items-center justify-center p-6">
      <section className="w-full max-w-md space-y-5">
        <h1 className="text-2xl font-semibold">
          {invited
            ? "Accept your invitation"
            : selfRecovering
              ? "Reset your password"
              : resetting
                ? "Recover your account"
                : "Finish account setup"}
        </h1>
        {email && !resetting && <p>{email}</p>}
        <p className="text-sm text-gray-600">
          {selfRecovering
            ? "Use both a current code from your authenticator and an unused recovery code saved when you enabled two-factor authentication. If you do not have both, contact your server administrator to reset your password."
            : invited
              ? "Choose your own password, then set up your authenticator and save your recovery codes. Your invitation can be used once and expires after 15 minutes."
              : resetting
                ? "Use the single-use recovery token supplied by your server operator. Choose your password here, then enroll your authenticator."
                : "Choose your own password and enable two-factor authentication before opening your account."}
        </p>
        {selfRecovering && (
          <p className="text-sm text-gray-600">
            Your authenticator stays enabled. This uses one recovery code and
            signs out all other sessions and app passwords.
          </p>
        )}
        {error && (
          <p role="alert" className="p-3 border border-red-300 text-red-800">
            {error}
          </p>
        )}
        {resetting || changingPassword ? (
          <form onSubmit={savePassword} className="space-y-4">
            {selfRecovering ? (
              <>
                <label className="block text-sm">
                  Email
                  <input
                    type="text"
                    inputMode="email"
                    autoCapitalize="none"
                    spellCheck={false}
                    autoComplete="username"
                    required
                    value={recoveryEmail}
                    onChange={(e) => setRecoveryEmail(e.target.value)}
                    className="mt-1 border p-3 w-full"
                  />
                </label>
                <label className="block text-sm">
                  Authenticator code
                  <input
                    autoComplete="one-time-code"
                    inputMode="numeric"
                    pattern="[0-9]{6}"
                    required
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    className="mt-1 border p-3 w-full"
                  />
                </label>
                <label className="block text-sm">
                  Recovery code
                  <input
                    type="password"
                    autoComplete="off"
                    required
                    value={recoveryCode}
                    onChange={(e) => setRecoveryCode(e.target.value)}
                    className="mt-1 border p-3 w-full"
                  />
                </label>
              </>
            ) : resetting ? (
              <label className="block text-sm">
                {invited ? "Invitation token" : "Recovery token"}
                <input
                  aria-label={invited ? "Invitation token" : "Recovery token"}
                  type="password"
                  autoComplete="off"
                  required
                  value={recovery?.token || ""}
                  onChange={(e) =>
                    setRecovery({
                      kind: invited ? "invitation" : "token",
                      token: e.target.value,
                    })
                  }
                  className="mt-1 border p-3 w-full"
                />
              </label>
            ) : (
              <label className="block text-sm">
                Current password
                <input
                  type="password"
                  autoComplete="current-password"
                  required
                  value={currentPassword}
                  onChange={(e) => setCurrentPassword(e.target.value)}
                  className="mt-1 border p-3 w-full"
                />
              </label>
            )}
            <label className="block text-sm">
              New password
              <input
                type="password"
                autoComplete="new-password"
                minLength={12}
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="mt-1 border p-3 w-full"
              />
            </label>
            <label className="block text-sm">
              Confirm new password
              <input
                type="password"
                autoComplete="new-password"
                minLength={12}
                required
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                className="mt-1 border p-3 w-full"
              />
            </label>
            <button
              disabled={busy}
              className="w-full bg-red-700 text-white p-3 disabled:opacity-50"
            >
              {busy
                ? "Saving…"
                : invited
                  ? "Accept invitation"
                  : resetting
                    ? "Set new password"
                    : "Change password"}
            </button>
          </form>
        ) : enrollment ? (
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (!savedCodes) return;
              void run(async () => {
                await post("/auth/2fa/confirm", { totp_code: code.trim() });
                // A reset token is not a password or a second factor. End the
                // setup session; a fresh sign-in proves the new password + TOTP.
                onSignOut();
                setNotice(
                  "Account setup complete. Sign in with your password and authenticator code.",
                );
                setEnrollment(null);
                setCode("");
                setSavedCodes(false);
              });
            }}
          >
            <AuthenticatorSetup
              secret={enrollment.secret}
              otpauthUrl={enrollment.otpauth_url}
            />
            <p className="text-sm">Account: {email || "REST Mail"}.</p>
            <p className="text-sm">
              Save these recovery codes somewhere safe. Each works once if you
              lose your authenticator.
            </p>
            <pre className="border p-3 whitespace-pre-wrap select-all">
              {enrollment.recovery_codes.join("\n")}
            </pre>
            <label className="block text-sm">
              <input
                type="checkbox"
                checked={savedCodes}
                onChange={(e) => setSavedCodes(e.target.checked)}
              />{" "}
              I have saved my recovery codes
            </label>
            <label className="block text-sm">
              Authenticator code
              <input
                autoComplete="one-time-code"
                inputMode="numeric"
                pattern="[0-9]{6}"
                required
                value={code}
                onChange={(e) => setCode(e.target.value)}
                className="mt-1 border p-3 w-full"
              />
            </label>
            <button
              disabled={busy || !savedCodes}
              className="w-full bg-red-700 text-white p-3 disabled:opacity-50"
            >
              {busy ? "Verifying…" : "Verify and continue"}
            </button>
          </form>
        ) : (
          <button
            disabled={busy}
            className="w-full bg-red-700 text-white p-3 disabled:opacity-50"
            onClick={() =>
              void run(async () => {
                setEnrollment(await post("/auth/2fa/enroll"));
                setCode("");
                setSavedCodes(false);
              })
            }
          >
            {busy ? "Starting…" : "Set up authenticator"}
          </button>
        )}
        <button
          disabled={busy}
          className="text-sm underline"
          onClick={() => {
            setRecovery(null);
            setPassword("");
            setConfirmPassword("");
            setCurrentPassword("");
            setRecoveryEmail("");
            setRecoveryCode("");
            setEnrollment(null);
            setCode("");
            setSavedCodes(false);
            setError("");
            setNotice("");
            onSignOut();
          }}
        >
          Back to sign in
        </button>
      </section>
    </main>
  );
}
