import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { JsonResponse } from "../../lib/api-contract";
import { DomainDNS } from "./DomainDNS";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("renders DNS records from the generated data envelope", async () => {
  const body: JsonResponse<"domainDNSCheck", 200> = {
    data: {
      domain: "example.com",
      records: [
        {
          type: "MX",
          name: "example.com",
          value: "10 mail.example.com",
          status: "ok",
        },
      ],
      summary: "All records verified",
    },
  };
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body)));
  vi.stubGlobal("fetch", fetch);
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(await screen.findByText("10 mail.example.com")).toBeTruthy();
  expect(screen.getByText("Verified")).toBeTruthy();
  expect(fetch.mock.calls[0][0]).toBe("https://api.test/admin/domains/7/dns");
});
