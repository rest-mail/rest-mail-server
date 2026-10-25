import { create } from "zustand";
import { apiV1 } from "../api";
import type { JsonResponse, RequestBody } from "../api-contract";
import { expectSuccess, readJSON } from "../api-contract";

type Mailbox = JsonResponse<"listMailboxes", 200>["data"][number];

interface MailboxState {
  mailboxes: Mailbox[];
  isLoading: boolean;
  error: string | null;
  selectedDomain: string | null;

  // Actions
  fetchMailboxes: () => Promise<void>;
  createMailbox: (data: {
    email: string;
    // Optional: an account with no password can't be signed in to, and is
    // reached only through its owners (the account creating it).
    password?: string;
    display_name?: string;
    quota_bytes?: number;
  }) => Promise<void>;
  updateMailbox: (
    id: number,
    data: RequestBody<"updateMailbox">,
  ) => Promise<Mailbox>;
  deleteMailbox: (id: number) => Promise<void>;
  setSelectedDomain: (domain: string | null) => void;
  clearError: () => void;
}

export const useMailboxStore = create<MailboxState>((set, get) => ({
  mailboxes: [],
  isLoading: false,
  error: null,
  selectedDomain: null,

  fetchMailboxes: async () => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/mailboxes", {
        method: "GET",
      });

      await expectSuccess(response, "Failed to fetch mailboxes");

      const response_data: JsonResponse<"listMailboxes", 200> =
        await response.json();
      const data = response_data.data;
      set({
        mailboxes: data,
        isLoading: false,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to fetch mailboxes",
        isLoading: false,
      });
      throw error;
    }
  },

  createMailbox: async (data) => {
    set({ isLoading: true, error: null });

    try {
      // An account is created the same way for everyone: POST /accounts, which
      // needs mailboxes:write on its domain and makes the creator its owner.
      const response = await apiV1.request("/accounts", {
        method: "POST",
        body: JSON.stringify({
          address: data.email,
          display_name: data.display_name || undefined,
          password: data.password || undefined,
        } satisfies RequestBody<"createAccount">),
      });

      if (!response.ok) {
        const error = await response.json().catch(() => ({}));
        throw new Error(
          error?.error?.message || "Failed to create the account",
        );
      }
      const created: JsonResponse<"createAccount", 201> = await response.json();
      const id = created.data.id;
      if (data.quota_bytes !== undefined) {
        const updated = await apiV1.request(`/admin/mailboxes/${id}`, {
          method: "PATCH",
          body: JSON.stringify({
            quota_bytes: data.quota_bytes,
          } satisfies RequestBody<"updateMailbox">),
        });
        await expectSuccess(
          updated,
          "Account created, but the quota update failed",
        );
      }

      // Refresh the list
      await get().fetchMailboxes();
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to create the account",
        isLoading: false,
      });
      throw error;
    }
  },

  updateMailbox: async (id, body) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(`/admin/mailboxes/${id}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      });
      const { data } = await readJSON<JsonResponse<"updateMailbox", 200>>(
        response,
        "Failed to update mailbox",
      );
      set({
        mailboxes: get().mailboxes.map((mailbox) =>
          mailbox.id === id ? data : mailbox,
        ),
        isLoading: false,
      });
      return data;
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to update mailbox",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteMailbox: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/mailboxes/${id}`, {
        method: "DELETE",
      });

      await expectSuccess(response, "Failed to delete mailbox");

      // Refresh the list
      await get().fetchMailboxes();
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to delete mailbox",
        isLoading: false,
      });
      throw error;
    }
  },

  setSelectedDomain: (domain) => {
    set({ selectedDomain: domain });
  },

  clearError: () => {
    set({ error: null });
  },
}));
