import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { InvitationCard, invitationLink } from "./InvitationCard";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("account invitations", () => {
  it("keeps the secret only in a fragment and rejects unsafe webmail addresses", () => {
    const link = new URL(
      invitationLink(
        "https://mail.example.com/?old=1#stale",
        "one-time-private-token",
      ),
    );
    expect(link.search).toBe("");
    expect(link.hash).toBe("#invite_token=one-time-private-token");
    expect(link.pathname).toBe("/");
    for (const base of [
      "not a URL",
      "javascript:alert(1)",
      "http://mail.example.com",
      "https://user:password@mail.example.com",
    ]) {
      expect(() => invitationLink(base, "token")).toThrow();
    }
    expect(invitationLink("http://localhost:5173", "token")).toContain(
      "#invite_token=token",
    );
  });
  it("issues one scoped request after an operator supplies the webmail address", async () => {
    const fetcher = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            token: "private-invitation-token",
            expires_at: "2026-10-26T12:00:00Z",
          },
        }),
        { status: 201 },
      ),
    );
    vi.stubGlobal("fetch", fetcher);
    render(<InvitationCard accountId={12} active />);
    expect(fetcher).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("Webmail address"), {
      target: { value: "https://mail.example.com" },
    });
    fireEvent.submit(screen.getByText("Create invitation").closest("form")!);
    await waitFor(() =>
      expect(screen.getByLabelText("Private invitation link")).toBeTruthy(),
    );
    expect(fetcher.mock.calls[0][0]).toBe(
      "https://api.test/admin/mailboxes/12/invitation",
    );
    expect(fetcher.mock.calls[0][1].method).toBe("POST");
    expect(fetcher.mock.calls[0][1].credentials).toBe("include");
    expect(
      (screen.getByLabelText("Private invitation link") as HTMLInputElement)
        .value,
    ).toBe("https://mail.example.com/#invite_token=private-invitation-token");
  });
  it("shows an established-account refusal without producing a link", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: {
              code: "account_not_invitable",
              message: "Only a new account may be invited",
            },
          }),
          { status: 409 },
        ),
      ),
    );
    render(<InvitationCard accountId={12} active />);
    fireEvent.change(screen.getByLabelText("Webmail address"), {
      target: { value: "https://mail.example.com" },
    });
    fireEvent.submit(screen.getByText("Create invitation").closest("form")!);
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "Only a new account",
      ),
    );
    expect(screen.queryByLabelText("Private invitation link")).toBeNull();
  });
});
