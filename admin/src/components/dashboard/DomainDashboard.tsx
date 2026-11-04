import { useCallback, useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import {
  AlertCircle,
  CheckCircle,
  FileText,
  Globe,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import { apiV1 } from "../../lib/api";
import { readJSON } from "../../lib/api-contract";
import type { components } from "../../lib/api-types";

type Domain = components["schemas"]["Domain"];
type DNSCheck = components["schemas"]["DomainDNSCheck"];
type Listed = { pagination?: components["schemas"]["Pagination"] | null };

// A part of the dashboard that loads on its own: one failing does not take the
// others with it.
type Part<T> =
  | { state: "loading" }
  | { state: "ready"; value: T }
  | { state: "failed"; message: string };

const failed = (error: unknown, fallback: string) => ({
  state: "failed" as const,
  message: error instanceof Error ? error.message : fallback,
});

async function reportCount(path: string, fallback: string): Promise<number> {
  const body = await readJSON<Listed>(await apiV1.request(`${path}?limit=1`), fallback);
  return body.pagination?.total ?? 0;
}

// DomainDashboard is the dashboard of an account that administers some
// domains rather than the whole server: its domains, each one's DNS check, and
// how many DMARC and TLS reports have arrived for them. Every route it reads
// accepts domains:read at a domain's scope and answers only that domain's rows.
export function DomainDashboard() {
  const [domains, setDomains] = useState<Part<Domain[]>>({ state: "loading" });
  const [dmarc, setDmarc] = useState<Part<number>>({ state: "loading" });
  const [tls, setTls] = useState<Part<number>>({ state: "loading" });

  const load = useCallback(() => {
    setDomains({ state: "loading" });
    setDmarc({ state: "loading" });
    setTls({ state: "loading" });
    void (async () => {
      try {
        const body = await readJSON<{ data: Domain[] }>(
          await apiV1.request("/admin/domains"),
          "Could not load your domains",
        );
        setDomains({ state: "ready", value: body.data });
      } catch (error) {
        setDomains(failed(error, "Could not load your domains"));
      }
    })();
    reportCount("/admin/dmarc-reports", "Could not count DMARC reports")
      .then((value) => setDmarc({ state: "ready", value }))
      .catch((error) => setDmarc(failed(error, "Could not count DMARC reports")));
    reportCount("/admin/tls-reports", "Could not count TLS reports")
      .then((value) => setTls({ state: "ready", value }))
      .catch((error) => setTls(failed(error, "Could not count TLS reports")));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1
            style={{ fontFamily: "Space Grotesk, sans-serif", color: "var(--black-soft)" }}
            className="text-2xl font-bold"
          >
            Dashboard
          </h1>
          <p style={{ color: "var(--gray-secondary)" }} className="text-sm mt-1">
            The domains you administer
          </p>
        </div>
        <button
          onClick={load}
          className="flex items-center gap-2 px-4 py-2 rounded-lg"
          style={{ border: "1px solid var(--gray-border)", color: "var(--black-soft)" }}
        >
          <RefreshCw className="w-4 h-4" />
          Refresh
        </button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
        <CountCard
          icon={<Globe className="w-6 h-6" />}
          label="Your domains"
          part={domains.state === "ready" ? { state: "ready", value: domains.value.length } : domains}
        />
        <CountCard icon={<FileText className="w-6 h-6" />} label="DMARC reports" part={dmarc} />
        <CountCard icon={<ShieldCheck className="w-6 h-6" />} label="TLS reports" part={tls} />
      </div>

      <div className="p-6 rounded-lg" style={{ border: "1px solid var(--gray-border)" }}>
        <h2
          style={{ fontFamily: "Space Grotesk, sans-serif", color: "var(--black-soft)" }}
          className="text-xl font-semibold mb-4"
        >
          DNS checks
        </h2>
        {domains.state === "loading" && (
          <p style={{ color: "var(--gray-secondary)" }} className="text-sm">
            Loading your domains...
          </p>
        )}
        {domains.state === "failed" && (
          <p style={{ color: "var(--red-primary)" }} className="text-sm">
            {domains.message}
          </p>
        )}
        {domains.state === "ready" && domains.value.length === 0 && (
          <p style={{ color: "var(--gray-secondary)" }} className="text-sm">
            You don't administer any domains yet.
          </p>
        )}
        {domains.state === "ready" && domains.value.length > 0 && (
          <ul className="divide-y" style={{ borderColor: "var(--gray-border)" }}>
            {domains.value.map((domain) => (
              <DomainRow key={domain.id} domain={domain} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function DomainRow({ domain }: { domain: Domain }) {
  const [check, setCheck] = useState<Part<DNSCheck>>({ state: "loading" });

  useEffect(() => {
    let live = true;
    void (async () => {
      try {
        const body = await readJSON<{ data: DNSCheck }>(
          await apiV1.request(`/admin/domains/${domain.id}/dns`),
          "Could not check DNS",
        );
        if (live) setCheck({ state: "ready", value: body.data });
      } catch (error) {
        if (live) setCheck(failed(error, "Could not check DNS"));
      }
    })();
    return () => {
      live = false;
    };
  }, [domain.id]);

  return (
    <li className="py-3 flex items-center justify-between gap-4" data-testid={`domain-${domain.name}`}>
      <Link
        to="/domains/$id"
        params={{ id: domain.id.toString() }}
        className="font-medium hover:underline"
        style={{ color: "var(--black-soft)" }}
      >
        {domain.name}
      </Link>
      <DNSStatus check={check} />
    </li>
  );
}

function DNSStatus({ check }: { check: Part<DNSCheck> }) {
  if (check.state === "loading") {
    return <span style={{ color: "var(--gray-secondary)" }} className="text-sm">Checking DNS...</span>;
  }
  if (check.state === "failed") {
    return <span style={{ color: "var(--red-primary)" }} className="text-sm">{check.message}</span>;
  }
  const records = check.value.records ?? [];
  const pending = records.filter((record) => record.status !== "ok").length;
  if (pending === 0) {
    return (
      <span className="text-sm flex items-center gap-1" style={{ color: "var(--green-success)" }}>
        <CheckCircle className="w-4 h-4" /> All {records.length} DNS records verified
      </span>
    );
  }
  return (
    <span className="text-sm flex items-center gap-1" style={{ color: "var(--red-primary)" }}>
      <AlertCircle className="w-4 h-4" /> {pending} of {records.length} DNS records need attention
    </span>
  );
}

function CountCard({ icon, label, part }: { icon: React.ReactNode; label: string; part: Part<number> }) {
  return (
    <div className="p-6 rounded-lg" style={{ border: "1px solid var(--gray-border)" }}>
      <div className="p-2 rounded-lg inline-block mb-4" style={{ backgroundColor: "var(--bg-surface)" }}>
        <div style={{ color: "var(--black-soft)" }}>{icon}</div>
      </div>
      {part.state === "ready" ? (
        <p style={{ fontFamily: "Space Grotesk, sans-serif", color: "var(--black-soft)" }} className="text-3xl font-bold mb-1">
          {part.value.toLocaleString()}
        </p>
      ) : (
        <p style={{ color: part.state === "failed" ? "var(--red-primary)" : "var(--gray-secondary)" }} className="text-sm mb-1">
          {part.state === "failed" ? part.message : "Loading..."}
        </p>
      )}
      <p style={{ color: "var(--gray-secondary)" }} className="text-sm">
        {label}
      </p>
    </div>
  );
}
