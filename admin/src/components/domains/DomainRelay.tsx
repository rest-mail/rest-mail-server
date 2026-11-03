import { useCallback, useEffect, useId, useState } from "react";
import { getRelay, setRelay } from "../../lib/domainPolicy";
import type { RelayView, SetRelayBody } from "../../lib/domainPolicy";
import { SectionError, SectionHeading } from "./sectionParts";
import { fieldClass, fieldStyle, labelStyle } from "./sectionStyles";

interface DomainRelayProps {
  domainId: number;
  /** Changing the relay needs domains:write; without it the settings are read-only. */
  canEdit: boolean;
}

type TLSMode = NonNullable<RelayView["tls_mode"]>;

interface RelayForm {
  transport: RelayView["transport"];
  host: string;
  port: string;
  tlsMode: TLSMode;
  username: string;
  password: string;
  removePassword: boolean;
}

function formFrom(relay: RelayView): RelayForm {
  return {
    transport: relay.transport,
    host: relay.host ?? "",
    port: relay.port ? String(relay.port) : "587",
    tlsMode: relay.tls_mode ?? "starttls",
    username: relay.username ?? "",
    password: "",
    removePassword: false,
  };
}

/** The body for a save. A blank password keeps the stored one; the API never returns it. */
function relayBody(form: RelayForm): SetRelayBody {
  if (form.transport === "direct") return { transport: "direct" };
  const password = form.removePassword
    ? { password: "" }
    : form.password
      ? { password: form.password }
      : {};
  return {
    transport: "relay",
    host: form.host.trim(),
    port: Number(form.port),
    tls_mode: form.tlsMode,
    username: form.username.trim(),
    ...password,
  };
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs mb-1" style={labelStyle}>
        {label}
      </dt>
      <dd className="text-sm" style={{ color: "var(--black-soft)" }}>
        {value}
      </dd>
    </div>
  );
}

/**
 * How this domain's outbound mail leaves: straight to each recipient's server, or
 * through a relay — the way out when a provider blocks port 25.
 */
export function DomainRelay({ domainId, canEdit }: DomainRelayProps) {
  const [relay, setRelayView] = useState<RelayView | null>(null);
  const [form, setForm] = useState<RelayForm | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const id = useId();

  const load = useCallback(async () => {
    setError(null);
    try {
      setRelayView(await getRelay(domainId));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load the relay settings");
    }
  }, [domainId]);

  useEffect(() => {
    load();
  }, [load]);

  const update = <K extends keyof RelayForm>(key: K, value: RelayForm[K]) =>
    setForm((current) => (current ? { ...current, [key]: value } : current));

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form) return;
    setBusy(true);
    setError(null);
    try {
      setRelayView(await setRelay(domainId, relayBody(form)));
      setForm(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save the relay settings");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section aria-labelledby={`relay-${domainId}`}>
      <div className="flex items-center justify-between">
        <SectionHeading id={`relay-${domainId}`}>Outbound relay</SectionHeading>
        {canEdit && relay && !form && (
          <button
            type="button"
            onClick={() => setForm(formFrom(relay))}
            className="h-10 px-6 text-sm font-medium border mb-4"
            style={{ borderColor: "var(--gray-border)", color: "var(--black-soft)", fontFamily: "Space Grotesk" }}
          >
            Change relay
          </button>
        )}
      </div>
      {error && <SectionError message={error} onRetry={relay ? undefined : load} />}
      {!relay && !error && (
        <p className="text-sm" style={labelStyle}>
          Loading relay settings...
        </p>
      )}

      {relay && !form && (
        <dl className="flex flex-col gap-4">
          <Row
            label="TRANSPORT"
            value={relay.transport === "direct" ? "Direct to each recipient's server" : "Through a relay"}
          />
          {relay.transport === "relay" && (
            <>
              <Row label="HOST" value={`${relay.host ?? ""}:${relay.port ?? ""}`} />
              <Row label="TLS" value={relay.tls_mode === "implicit" ? "Implicit TLS" : "STARTTLS"} />
              <Row label="USERNAME" value={relay.username || "None"} />
              <Row label="PASSWORD" value={relay.password_set ? "Set" : "Not set"} />
            </>
          )}
        </dl>
      )}

      {form && (
        <form onSubmit={save} className="flex flex-col gap-4">
          <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
            Transport
            <select
              className={fieldClass}
              style={fieldStyle}
              value={form.transport}
              onChange={(e) => update("transport", e.target.value as RelayForm["transport"])}
            >
              <option value="direct">Direct to each recipient's server</option>
              <option value="relay">Through a relay</option>
            </select>
          </label>
          {form.transport === "relay" && (
            <>
              <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
                Host
                <input
                  required
                  className={fieldClass}
                  style={fieldStyle}
                  value={form.host}
                  onChange={(e) => update("host", e.target.value)}
                  placeholder="smtp.example.net"
                />
              </label>
              <div className="flex gap-4">
                <label className="flex-1 flex flex-col gap-1 text-xs" style={labelStyle}>
                  Port
                  <input
                    required
                    type="number"
                    min={1}
                    max={65535}
                    className={fieldClass}
                    style={fieldStyle}
                    value={form.port}
                    onChange={(e) => update("port", e.target.value)}
                  />
                </label>
                <label className="flex-1 flex flex-col gap-1 text-xs" style={labelStyle}>
                  TLS
                  <select
                    className={fieldClass}
                    style={fieldStyle}
                    value={form.tlsMode}
                    onChange={(e) => update("tlsMode", e.target.value as TLSMode)}
                  >
                    <option value="starttls">STARTTLS</option>
                    <option value="implicit">Implicit TLS</option>
                  </select>
                </label>
              </div>
              <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
                Username
                <input
                  className={fieldClass}
                  style={fieldStyle}
                  value={form.username}
                  onChange={(e) => update("username", e.target.value)}
                  autoComplete="off"
                />
              </label>
              <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
                Password
                <input
                  type="password"
                  className={fieldClass}
                  style={fieldStyle}
                  value={form.password}
                  disabled={form.removePassword}
                  onChange={(e) => update("password", e.target.value)}
                  autoComplete="new-password"
                  aria-describedby={`${id}-password-help`}
                />
              </label>
              <p id={`${id}-password-help`} className="text-xs" style={labelStyle}>
                {relay?.password_set
                  ? "A password is set. Leave this blank to keep it."
                  : "No password is set."}
              </p>
              {relay?.password_set && (
                <label className="flex items-center gap-2 text-sm" style={{ color: "var(--black-soft)" }}>
                  <input
                    type="checkbox"
                    checked={form.removePassword}
                    onChange={(e) => update("removePassword", e.target.checked)}
                    className="w-4 h-4"
                    style={{ accentColor: "var(--red-primary)" }}
                  />
                  Remove the stored password
                </label>
              )}
            </>
          )}
          <div className="flex gap-3">
            <button
              type="submit"
              disabled={busy}
              className="h-10 px-6 text-white text-sm font-medium"
              style={{ backgroundColor: "var(--red-primary)", fontFamily: "Space Grotesk", opacity: busy ? 0.6 : 1 }}
            >
              Save relay
            </button>
            <button
              type="button"
              onClick={() => {
                setForm(null);
                setError(null);
              }}
              className="h-10 px-6 text-sm font-medium border"
              style={{ borderColor: "var(--gray-border)", color: "var(--gray-secondary)", fontFamily: "Space Grotesk" }}
            >
              Cancel
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
