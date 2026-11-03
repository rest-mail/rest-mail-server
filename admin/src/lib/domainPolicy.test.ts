import { expect, it } from "vitest";
import { dmarcCountsBySource, holdsCapability } from "./domainPolicy";
import type { DmarcReport } from "./domainPolicy";

function report(rows: Array<[string, number, "pass" | "fail", "pass" | "fail"]>): DmarcReport {
  return {
    id: 1,
    domain_id: 7,
    report_id: "r1",
    reporter: "google.com",
    reporting_org: "Google",
    start_date: "2026-11-01T00:00:00Z",
    end_date: "2026-11-02T00:00:00Z",
    received_at: "2026-11-02T01:00:00Z",
    policy_published: { domain: "example.com", p: "none" },
    records: rows.map(([source_ip, count, dkim, spf]) => ({
      row: { source_ip, count, policy_evaluated: { disposition: "none", dkim, spf } },
      identifiers: { header_from: "example.com" },
      auth_results: {},
    })),
  };
}

it("counts a message as passing when DKIM or SPF passed, failing sources first", () => {
  const counts = dmarcCountsBySource([
    report([["192.0.2.1", 10, "pass", "fail"], ["198.51.100.9", 3, "fail", "fail"]]),
    report([["192.0.2.1", 5, "fail", "pass"], ["198.51.100.9", 2, "fail", "pass"]]),
  ]);
  expect(counts).toEqual([
    { source: "198.51.100.9", pass: 2, fail: 3 },
    { source: "192.0.2.1", pass: 15, fail: 0 },
  ]);
});

it("holds a capability by name or through *", () => {
  expect(holdsCapability(["domains:read"], "domains:read")).toBe(true);
  expect(holdsCapability(["*"], "domains:write")).toBe(true);
  expect(holdsCapability(["domains:read"], "domains:write")).toBe(false);
  expect(holdsCapability(undefined, "domains:read")).toBe(false);
});
