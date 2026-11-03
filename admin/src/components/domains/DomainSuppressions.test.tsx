import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { JsonResponse } from "../../lib/api-contract";
import { DomainSuppressions } from "./DomainSuppressions";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const list: JsonResponse<"listSuppressions", 200> = {
  data: [
    {
      id: 3,
      domain_id: 7,
      address: "bob@remote.example",
      reason: "hard_bounce",
      failure_category: "bad_mailbox",
      last_error: "550 5.1.1 user unknown",
      message_id: null,
      created_at: "2026-11-01T00:00:00Z",
      updated_at: "2026-11-01T00:00:00Z",
      expires_at: "2027-01-30T00:00:00Z",
    },
  ],
};

function stub(clear: () => Response) {
  const fetch = vi.fn(async (_url: string, init?: RequestInit) =>
    init?.method === "DELETE" ? clear() : new Response(JSON.stringify(list)),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

it("clears a suppression after the confirm dialog", async () => {
  const fetch = stub(() => new Response(null, { status: 204 }));
  render(<DomainSuppressions domainId={7} canClear />);

  expect(await screen.findByText("550 5.1.1 user unknown")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Clear bob@remote.example" }));
  expect(screen.getByRole("alertdialog")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Clear suppression" }));

  await waitFor(() => expect(screen.queryByText("bob@remote.example")).toBeNull());
  expect(screen.getByText("No addresses are suppressed.")).toBeTruthy();
  expect(fetch.mock.calls[0][0]).toBe("https://api.test/admin/domains/7/suppressions");
  expect(fetch.mock.calls[1][0]).toBe("https://api.test/admin/domains/7/suppressions/3");
  expect(fetch.mock.calls[1][1]?.method).toBe("DELETE");
});

it("cancelling the dialog sends nothing", async () => {
  const fetch = stub(() => new Response(null, { status: 204 }));
  render(<DomainSuppressions domainId={7} canClear />);
  fireEvent.click(await screen.findByRole("button", { name: "Clear bob@remote.example" }));
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.queryByRole("alertdialog")).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("keeps the address and shows the refusal when clearing fails", async () => {
  stub(() => new Response(JSON.stringify({ error: { code: "not_found", message: "Suppression not found" } }), { status: 404 }));
  render(<DomainSuppressions domainId={7} canClear />);
  fireEvent.click(await screen.findByRole("button", { name: "Clear bob@remote.example" }));
  fireEvent.click(screen.getByRole("button", { name: "Clear suppression" }));
  expect((await screen.findByRole("alert")).textContent).toContain("Suppression not found");
  expect(screen.getByText("bob@remote.example")).toBeTruthy();
});

it("offers no clear without domains:write", async () => {
  stub(() => new Response(null, { status: 204 }));
  render(<DomainSuppressions domainId={7} canClear={false} />);
  expect(await screen.findByText("bob@remote.example")).toBeTruthy();
  expect(screen.queryByRole("button", { name: /Clear/ })).toBeNull();
});
