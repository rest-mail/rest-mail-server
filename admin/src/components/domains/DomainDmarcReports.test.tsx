import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { JsonResponse } from "../../lib/api-contract";
import { DomainDmarcReports } from "./DomainDmarcReports";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const body: JsonResponse<"listDmarcReports", 200> = {
  data: [
    {
      id: 4,
      domain_id: 7,
      report_id: "r4",
      reporter: "google.com",
      reporting_org: "Google",
      start_date: "2026-11-01T00:00:00Z",
      end_date: "2026-11-02T00:00:00Z",
      received_at: "2026-11-02T01:00:00Z",
      policy_published: { domain: "example.com", p: "none" },
      records: [
        {
          row: { source_ip: "192.0.2.1", count: 12, policy_evaluated: { disposition: "none", dkim: "pass", spf: "pass" } },
          identifiers: { header_from: "example.com" },
          auth_results: {},
        },
        {
          row: { source_ip: "198.51.100.9", count: 4, policy_evaluated: { disposition: "none", dkim: "fail", spf: "fail" } },
          identifiers: { header_from: "example.com" },
          auth_results: {},
        },
      ],
    },
  ],
  pagination: { has_more: false, total: 1 },
};

it("lists the domain's reports with pass and fail counts by source", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body)));
  vi.stubGlobal("fetch", fetch);
  render(<DomainDmarcReports domainId={7} />);

  expect(await screen.findByText("1 source fails DMARC. Fix or account for it before moving to quarantine or reject.")).toBeTruthy();
  const failing = screen.getByText("198.51.100.9").closest("tr")!;
  expect(within(failing).getAllByRole("cell").map((c) => c.textContent)).toEqual(["198.51.100.9", "0", "4"]);
  const passing = screen.getByText("192.0.2.1").closest("tr")!;
  expect(within(passing).getAllByRole("cell").map((c) => c.textContent)).toEqual(["192.0.2.1", "12", "0"]);
  expect(screen.getByText("Google")).toBeTruthy();
  expect(screen.getByText("p=none")).toBeTruthy();
  expect(fetch.mock.calls[0][0]).toBe("https://api.test/admin/dmarc-reports?domain_id=7&limit=50");
});

it("says when no reports have arrived", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: [] }))));
  render(<DomainDmarcReports domainId={7} />);
  expect(await screen.findByText(/No reports yet/)).toBeTruthy();
});

it("shows the API's refusal", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "forbidden", message: "Not allowed here" } }), { status: 403 }),
    ),
  );
  render(<DomainDmarcReports domainId={7} />);
  expect((await screen.findByRole("alert")).textContent).toContain("Not allowed here");
});
