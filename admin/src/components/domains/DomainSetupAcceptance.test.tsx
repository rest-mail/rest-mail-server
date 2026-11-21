import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ComponentType, ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useAuthStore } from "../../lib/stores/authStore";
import { useDomainStore } from "../../lib/stores/domainStore";
import { Route } from "../../routes/domains/new";
import { ScreenGate } from "../access/ScreenGate";
import { DomainDNS } from "./DomainDNS";

const router = vi.hoisted(() => ({ pathname: "/domains/new", navigate: vi.fn() }));
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  useNavigate: () => router.navigate,
  useRouterState: ({ select }: { select: (state: unknown) => unknown }) =>
    select({ location: { pathname: router.pathname } }),
  Link: ({ to, children }: { to: string; children: ReactNode }) => <a href={to}>{children}</a>,
}));
vi.mock("../layout/AppShell", () => ({
  AppShell: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

const NewDomain = Route.options.component as ComponentType;

beforeEach(() => {
  router.pathname = "/domains/new";
  router.navigate.mockReset();
  useDomainStore.setState({ domains: [], error: null, isLoading: false });
  useAuthStore.setState({
    user: { email: "owner@customer.test", capabilities: ["*"], scopedCapabilities: [] },
    accessToken: "cookie-session", isAuthenticated: true,
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it.each(["example..com", ".example.com", "", ".."])(
  "AC01 shows the API's field refusal for %j",
  async (name) => {
    const message = name === "" ? "required" : "Invalid domain name: empty label";
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: "validation_failed", message: "Request body failed validation", details: { fields: { name: message } } },
    }), { status: 422 }));
    vi.stubGlobal("fetch", fetcher);
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<NewDomain />);
    fireEvent.change(screen.getByPlaceholderText("example.com"), { target: { value: name } });
    fireEvent.click(screen.getByRole("button", { name: "Create Domain" }));
    expect(await screen.findByText(message)).toBeTruthy();
    expect(JSON.parse(fetcher.mock.calls[0][1].body).name).toBe(name);
    expect(router.navigate).not.toHaveBeenCalled();
  },
);

it.each(["EXAMPLE.COM.", "münchen.test."])("AC01 passes %s through to server normalization", async (name) => {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    data: { id: 7, name: new URL(`https://${name}`).hostname.slice(0, -1) },
  }), { status: 201 }));
  vi.stubGlobal("fetch", fetcher);
  render(<NewDomain />);
  fireEvent.change(screen.getByPlaceholderText("example.com"), { target: { value: name } });
  fireEvent.click(screen.getByRole("button", { name: "Create Domain" }));
  await waitFor(() => expect(router.navigate).toHaveBeenCalledWith({ to: "/domains" }));
  expect(JSON.parse(fetcher.mock.calls[0][1].body).name).toBe(name);
});

it("AC02 opens the delegated DNS screen but does not mount global domain creation", async () => {
  useAuthStore.setState({ user: {
    email: "delegate@customer.test", capabilities: [], scopedCapabilities: [
      { capability: "domains:read", scope: "domain:customer.test" },
      { capability: "domains:write", scope: "domain:customer.test" },
    ],
  } });
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: {
    domain: "customer.test", summary: "0/1 records verified",
    records: [{ type: "MX", name: "customer.test", status: "missing", expected: "10 mail.customer.test" }],
  } })));
  vi.stubGlobal("fetch", fetcher);
  const gated = render(<ScreenGate><NewDomain /></ScreenGate>);
  expect(screen.getByText("You don't have access to this")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Create Domain" })).toBeNull();
  expect(fetcher).not.toHaveBeenCalled();
  router.pathname = "/domains/7";
  gated.rerender(<ScreenGate><DomainDNS domainId={7} accessToken="cookie-session" /></ScreenGate>);
  expect(await screen.findByText("10 mail.customer.test")).toBeTruthy();
  expect(fetcher.mock.calls[0][0]).toBe("https://api.test/admin/domains/7/dns");
});
