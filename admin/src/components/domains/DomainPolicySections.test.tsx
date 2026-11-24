import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { DomainPolicySections } from "./DomainPolicySections";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("shows nothing and asks nothing without domains:read", () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const { container } = render(<DomainPolicySections domainId={7} domainName="customer.test" capabilities={["mail:read"]} />);
  expect(container.innerHTML).toBe("");
  expect(fetch).not.toHaveBeenCalled();
});

it("shows read-only sections with domains:read alone", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) =>
      new Response(
        JSON.stringify(
          String(url).endsWith("/relay")
            ? { data: { domain_id: 7, transport: "direct", password_set: false } }
            : { data: [] },
        ),
      ),
    ),
  );
  render(<DomainPolicySections domainId={7} domainName="customer.test" capabilities={["domains:read"]} />);
  expect(screen.getByRole("heading", { name: "DMARC reports" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Suppressed addresses" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Outbound relay" })).toBeTruthy();
  expect(await screen.findByText("Direct to each recipient's server")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Change relay" })).toBeNull();
});

it.each([
  { writeScope: "domain:customer.test", canClear: true },
  { writeScope: "domain:other.test", canClear: false },
])("loads delegated policy data with write scope $writeScope", async ({ writeScope, canClear }) => {
  const fetch = vi.fn(async (url: string) => new Response(JSON.stringify({
    data: url.endsWith("/relay")
      ? { domain_id: 7, transport: "direct", password_set: false }
      : url.endsWith("/suppressions")
        ? [{ id: 3, address: "bounce@example.test", expires_at: "2027-01-30T00:00:00Z" }]
        : [],
  })));
  vi.stubGlobal("fetch", fetch);
  render(<DomainPolicySections
    domainId={7}
    domainName="customer.test"
    capabilities={[]}
    scopedCapabilities={[
      { capability: "domains:read", scope: "domain:customer.test" },
      { capability: "domains:write", scope: writeScope },
    ]}
  />);

  expect(await screen.findByText("bounce@example.test")).toBeTruthy();
  expect(await screen.findByText("Direct to each recipient's server")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "DMARC reports" })).toBeTruthy();
  expect(Boolean(screen.queryByRole("button", { name: "Clear bounce@example.test" }))).toBe(canClear);
  expect(screen.queryByRole("button", { name: "Change relay" })).toBeNull();
  expect(fetch.mock.calls.map(([url]) => url).sort()).toEqual([
    "https://api.test/admin/dmarc-reports?domain_id=7&limit=50",
    "https://api.test/admin/domains/7/relay",
    "https://api.test/admin/domains/7/suppressions",
  ]);
});

it.each(["domain:other.test", "mailbox:delegate@customer.test"])(
  "does not load domain policy for the unrelated scope %s",
  (scope) => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const { container } = render(<DomainPolicySections
      domainId={7}
      domainName="customer.test"
      capabilities={[]}
      scopedCapabilities={[{ capability: "*", scope }]}
    />);
    expect(container.innerHTML).toBe("");
    expect(fetch).not.toHaveBeenCalled();
  },
);
