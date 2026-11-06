import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { JsonResponse } from "../../lib/api-contract";
import { ReputationTable } from "./ReputationTable";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const bounces = { bad_mailbox: 0, bad_domain: 0, policy: 0, other_permanent: 0, expired: 0 };
const nothing = {
  dmarc: { pass: 0, fail: 0, pass_rate: null },
  outbound: { delivered: 0, bounced: 0, deferred: 0, bounce_rate: null, bounces },
  complaints: null,
};

it("renders a row per domain, flags a bounce spike and links to the outbound stop", async () => {
  const body: JsonResponse<"getReputation", 200> = {
    data: {
      from: "2026-01-01T00:00:00Z",
      to: "2026-01-08T00:00:00Z",
      days: 7,
      dnsbl: {
        listed: true,
        listings: [{ ip: "203.0.113.10", provider: "example", zone: "bl.example.org", since: "2026-01-05T00:00:00Z", reason: "spam" }],
      },
      domains: [
        {
          domain_id: 3,
          domain: "noisy.example",
          outbound_stop: { stopped_at: "2026-01-07T00:00:00Z", stopped_by: "op@example.org", reason: "spike" },
          current: {
            dmarc: { pass: 3, fail: 9, pass_rate: 0.25 },
            outbound: { delivered: 1, bounced: 3, deferred: 1, bounce_rate: 0.75, bounces: { ...bounces, policy: 2, expired: 1 } },
            complaints: null,
          },
          previous: {
            dmarc: { pass: 5, fail: 0, pass_rate: 1 },
            outbound: { delivered: 4, bounced: 0, deferred: 0, bounce_rate: 0, bounces },
            complaints: null,
          },
        },
        { domain_id: 4, domain: "quiet.example", outbound_stop: null, current: nothing, previous: nothing },
      ],
    },
  };
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body)));
  vi.stubGlobal("fetch", fetch);
  render(<ReputationTable accessToken="cookie-session" />);

  const noisy = await screen.findByTestId("reputation-noisy.example");
  expect(within(noisy).getByText("75.0%")).toBeTruthy();
  expect(within(noisy).getByText("Spike")).toBeTruthy();
  expect(within(noisy).getByText("Drop")).toBeTruthy();
  expect(within(noisy).getByText("policy 2, expired 1")).toBeTruthy();
  expect(within(noisy).getByText("Stopped: restart…").getAttribute("href")).toBe("/domains/3#outbound-stop");

  const quiet = screen.getByTestId("reputation-quiet.example");
  expect(within(quiet).queryByText("Spike")).toBeNull();
  expect(within(quiet).getByText("Stop…").getAttribute("href")).toBe("/domains/4#outbound-stop");

  const rows = screen.getAllByTestId(/^reputation-/);
  expect(rows.map((r) => r.dataset.testid)).toEqual(["reputation-noisy.example", "reputation-quiet.example"]);
  expect(screen.getByTestId("dnsbl-state").textContent).toContain("203.0.113.10");
  expect(fetch.mock.calls[0][0]).toBe("https://api.test/admin/reputation?days=7");
});
