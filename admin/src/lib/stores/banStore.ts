import { create } from "zustand";
import { apiV1 } from "../api";
import type {
  JsonResponse,
  RequestBody,
  QueryParameters,
} from "../api-contract";
import { expectSuccess } from "../api-contract";

type Ban = JsonResponse<"listBans", 200>["data"][number];

interface BanState {
  bans: Ban[];
  isLoading: boolean;
  error: string | null;
  pagination: {
    total: number;
    hasMore: boolean;
  };

  // Actions
  fetchBans: (
    filters: Omit<QueryParameters<"listBans">, "active"> & { active?: boolean },
  ) => Promise<void>;
  createBan: (data: RequestBody<"createBan">) => Promise<void>;
  deleteBan: (id: number) => Promise<void>;
  deleteBanByIP: (ip: string) => Promise<void>;
  isExpired: (ban: Ban) => boolean;
  clearError: () => void;
}

export const useBanStore = create<BanState>((set) => ({
  bans: [],
  isLoading: false,
  error: null,
  pagination: { total: 0, hasMore: false },

  fetchBans: async (filters) => {
    set({ isLoading: true, error: null });
    try {
      const params = new URLSearchParams();
      if (filters.protocol) params.set("protocol", filters.protocol);
      if (filters.active !== undefined)
        params.set("active", String(filters.active));
      if (filters.limit) params.set("limit", String(filters.limit));
      if (filters.offset) params.set("offset", String(filters.offset));

      const response = await apiV1.request(`/admin/bans?${params.toString()}`, {
        method: "GET",
      });
      await expectSuccess(response, "Failed to fetch bans");
      const response_data: JsonResponse<"listBans", 200> =
        await response.json();
      const data = response_data.data;
      set({
        bans: data,
        pagination: {
          total: response_data.pagination?.total || 0,
          hasMore: response_data.pagination?.has_more || false,
        },
        isLoading: false,
      });
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Failed to fetch bans",
        isLoading: false,
      });
      throw error;
    }
  },

  createBan: async (data) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request("/admin/bans", {
        method: "POST",
        body: JSON.stringify(data),
        headers: { "Content-Type": "application/json" },
      });
      await expectSuccess(response, "Failed to create ban");
      set({ isLoading: false });
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Failed to create ban",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteBan: async (id: number) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(`/admin/bans/${id}`, {
        method: "DELETE",
      });
      await expectSuccess(response, "Failed to delete ban");
      set({ isLoading: false });
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Failed to delete ban",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteBanByIP: async (ip: string) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(
        `/admin/bans/ip/${encodeURIComponent(ip)}`,
        { method: "DELETE" },
      );
      await expectSuccess(response, "Failed to unban IP");
      set({ isLoading: false });
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Failed to unban IP",
        isLoading: false,
      });
      throw error;
    }
  },

  isExpired: (ban: Ban) => {
    if (!ban.expires_at) return false;
    return new Date(ban.expires_at) < new Date();
  },

  clearError: () => set({ error: null }),
}));
