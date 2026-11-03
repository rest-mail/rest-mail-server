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
  const { container } = render(<DomainPolicySections domainId={7} capabilities={["mail:read"]} />);
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
  render(<DomainPolicySections domainId={7} capabilities={["domains:read"]} />);
  expect(screen.getByRole("heading", { name: "DMARC reports" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Suppressed addresses" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Outbound relay" })).toBeTruthy();
  expect(await screen.findByText("Direct to each recipient's server")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Change relay" })).toBeNull();
});
