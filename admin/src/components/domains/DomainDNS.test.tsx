import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { JsonResponse } from "../../lib/api-contract";
import { DomainDNS } from "./DomainDNS";
import { DKIM_PROBLEM_TEXT } from "./dkimProblems";

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

function stubDNS(record: JsonResponse<"domainDNSCheck", 200>["data"]["records"][number]) {
  const body: JsonResponse<"domainDNSCheck", 200> = {
    data: { domain: "example.com", records: [record], summary: "" },
  };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
}

const dkimName = "mail._domainkey.example.com";
const dkimValue = "v=DKIM1; k=ed25519; p=11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=";

it("shows the DKIM record to publish and copies its value", async () => {
  stubDNS({ type: "DKIM", name: dkimName, status: "missing", expected: dkimValue, problem: "missing" });
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(await screen.findByText("No DKIM record is published yet.")).toBeTruthy();
  expect(screen.getByText("Publish this TXT record:")).toBeTruthy();
  expect(screen.getByText(dkimValue)).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Copy DKIM record value" }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(dkimValue));
  expect(await screen.findByText("Copied")).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Copy DKIM record name" }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(dkimName));
});

it.each([
  ["malformed", DKIM_PROBLEM_TEXT.malformed],
  ["different_key_type", DKIM_PROBLEM_TEXT.different_key_type],
  ["different_key", "The published record carries a different key."],
  ["more_than_one", DKIM_PROBLEM_TEXT.more_than_one],
] as const)("explains the DKIM problem %s", async (problem, text) => {
  stubDNS({
    type: "DKIM", name: dkimName, status: "different",
    value: "v=DKIM1; k=rsa; p=MIIB", expected: dkimValue, problem,
  });
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(await screen.findByText(text)).toBeTruthy();
  expect(screen.getByText(dkimValue)).toBeTruthy();
});

it("says mail is held when the DKIM key is unreadable", async () => {
  stubDNS({ type: "DKIM", name: dkimName, status: "error", problem: "key_unreadable" });
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(await screen.findByText(/mail for this domain is not being sent until the key is fixed/)).toBeTruthy();
  expect(screen.queryByText("Publish this TXT record:")).toBeNull();
  expect(screen.queryByRole("button", { name: "Copy DKIM record value" })).toBeNull();
});

it("shows the matching DKIM record without a problem line", async () => {
  stubDNS({ type: "DKIM", name: dkimName, status: "ok", value: dkimValue, expected: dkimValue });
  render(<DomainDNS domainId={7} accessToken="cookie-session" />);

  expect(await screen.findByText("The published TXT record matches:")).toBeTruthy();
  for (const text of Object.values(DKIM_PROBLEM_TEXT)) {
    expect(screen.queryByText(text)).toBeNull();
  }
});
