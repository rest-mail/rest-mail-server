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

const mockCheck = (records: JsonResponse<"domainDNSCheck", 200>["data"]["records"]) => {
  const body: JsonResponse<"domainDNSCheck", 200> = {
    data: { domain: "customer.test", records, summary: "" },
  };
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response(JSON.stringify(body))),
  );
};

it("shows the value to publish for a record that is not published yet", async () => {
  mockCheck([
    {
      type: "DMARC",
      name: "_dmarc.customer.test",
      status: "missing",
      expected: "v=DMARC1; p=none; rua=mailto:postmaster@customer.test",
      message: "No DMARC record found",
    },
  ]);
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(
    await screen.findByText(
      "v=DMARC1; p=none; rua=mailto:postmaster@customer.test",
    ),
  ).toBeTruthy();
  expect(screen.getByText("Publish this value")).toBeTruthy();
  expect(screen.getByText("Missing")).toBeTruthy();
  expect(screen.queryByText("Found")).toBeNull();
});

it("says when what is published differs, and shows both values", async () => {
  mockCheck([
    {
      type: "MX",
      name: "customer.test",
      status: "different",
      value: "10 sink.remote.test",
      expected: "10 mail.example.net",
      message: "Mail for this domain goes to sink.remote.test, not to this server.",
    },
  ]);
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(await screen.findByText("Different")).toBeTruthy();
  expect(screen.queryByText("Verified")).toBeNull();
  expect(screen.getByText("10 mail.example.net")).toBeTruthy();
  expect(screen.getByText("10 sink.remote.test")).toBeTruthy();
  expect(screen.getByText("Publish this value")).toBeTruthy();
  expect(screen.getByText("Found")).toBeTruthy();
});

it("shows a verified value once", async () => {
  mockCheck([
    {
      type: "SPF",
      name: "customer.test",
      status: "ok",
      value: "v=spf1 mx -all",
      expected: "v=spf1 mx -all",
    },
  ]);
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(await screen.findByText("v=spf1 mx -all")).toBeTruthy();
  expect(screen.getByText("Verified")).toBeTruthy();
  expect(screen.queryByText("Found")).toBeNull();
});

it("shows what it found when the server has no value to compare it with", async () => {
  mockCheck([
    {
      type: "A",
      name: "mail.example.net",
      status: "ok",
      value: "203.0.113.25",
      message: "REST Mail does not know its own public address.",
    },
  ]);
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(await screen.findByText("203.0.113.25")).toBeTruthy();
  expect(screen.getByText("Found")).toBeTruthy();
  expect(screen.queryByText("Publish this value")).toBeNull();
});
