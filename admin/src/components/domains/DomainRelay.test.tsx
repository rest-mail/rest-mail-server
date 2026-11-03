import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { JsonResponse } from "../../lib/api-contract";
import { DomainRelay } from "./DomainRelay";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const stored: JsonResponse<"getDomainRelay", 200> = {
  data: {
    domain_id: 7,
    transport: "relay",
    host: "smtp.example.net",
    port: 587,
    tls_mode: "starttls",
    username: "apikey",
    password_set: true,
  },
};

function stub(save: (init: RequestInit) => Response) {
  const fetch = vi.fn(async (_url: string, init?: RequestInit) =>
    init?.method === "PUT" ? save(init) : new Response(JSON.stringify(stored)),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

it("shows the relay and only whether a password is set", async () => {
  stub(() => new Response("{}"));
  render(<DomainRelay domainId={7} canEdit={false} />);

  expect(await screen.findByText("smtp.example.net:587")).toBeTruthy();
  expect(screen.getByText("STARTTLS")).toBeTruthy();
  expect(screen.getByText("apikey")).toBeTruthy();
  expect(screen.getByText("Set")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Change relay" })).toBeNull();
});

it("saves a changed relay, keeping the stored password when left blank", async () => {
  const fetch = stub(
    () =>
      new Response(
        JSON.stringify({ data: { ...stored.data, host: "relay.example.org", port: 465, tls_mode: "implicit" } }),
      ),
  );
  render(<DomainRelay domainId={7} canEdit />);

  fireEvent.click(await screen.findByRole("button", { name: "Change relay" }));
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "relay.example.org" } });
  fireEvent.change(screen.getByLabelText("Port"), { target: { value: "465" } });
  fireEvent.change(screen.getByLabelText("TLS"), { target: { value: "implicit" } });
  expect((screen.getByLabelText("Password") as HTMLInputElement).value).toBe("");
  fireEvent.click(screen.getByRole("button", { name: "Save relay" }));

  expect(await screen.findByText("relay.example.org:465")).toBeTruthy();
  const [url, init] = fetch.mock.calls[1];
  expect(url).toBe("https://api.test/admin/domains/7/relay");
  expect(JSON.parse(String(init?.body))).toEqual({
    transport: "relay",
    host: "relay.example.org",
    port: 465,
    tls_mode: "implicit",
    username: "apikey",
  });
});

it("switching to direct sends only the transport", async () => {
  const fetch = stub(() => new Response(JSON.stringify({ data: { domain_id: 7, transport: "direct", password_set: false } })));
  render(<DomainRelay domainId={7} canEdit />);
  fireEvent.click(await screen.findByRole("button", { name: "Change relay" }));
  fireEvent.change(screen.getByLabelText("Transport"), { target: { value: "direct" } });
  fireEvent.click(screen.getByRole("button", { name: "Save relay" }));
  expect(await screen.findByText("Direct to each recipient's server")).toBeTruthy();
  expect(JSON.parse(String(fetch.mock.calls[1][1]?.body))).toEqual({ transport: "direct" });
});

it("keeps the form and shows the refusal when the save fails", async () => {
  stub(() => new Response(JSON.stringify({ error: { code: "conflict", message: "No master key is configured" } }), { status: 409 }));
  render(<DomainRelay domainId={7} canEdit />);
  fireEvent.click(await screen.findByRole("button", { name: "Change relay" }));
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "new secret" } });
  fireEvent.click(screen.getByRole("button", { name: "Save relay" }));
  expect((await screen.findByRole("alert")).textContent).toContain("No master key is configured");
  expect(screen.getByRole("button", { name: "Save relay" })).toBeTruthy();
});
