import { create } from "zustand";
import { apiV1 } from "../api";
import { expectSuccess, readJSON } from "../api-contract";
import type { JsonResponse, RequestBody } from "../api-contract";

type Alias = JsonResponse<"listAliases", 200>["data"][number];

interface AliasState {
  aliases: Alias[];
  isLoading: boolean;
  error: string | null;
  selectedDomain: string | null;

  // Actions
  fetchAliases: (domainId?: string) => Promise<void>;
  createAlias: (data: RequestBody<"createAlias">) => Promise<void>;
  updateAlias: (id: number, data: RequestBody<"updateAlias">) => Promise<void>;
  deleteAlias: (id: number) => Promise<void>;
  setSelectedDomain: (domain: string | null) => void;
  clearError: () => void;
}

export const useAliasStore = create<AliasState>((set, get) => ({
  aliases: [],
  isLoading: false,
  error: null,
  selectedDomain: null,

  fetchAliases: async (domainId?: string) => {
    set({ isLoading: true, error: null });

    try {
      const url = domainId
        ? `/admin/aliases?domain_id=${domainId}`
        : "/admin/aliases";

      const response = await apiV1.request(url, { method: "GET" });

      const { data } = await readJSON<JsonResponse<"listAliases", 200>>(
        response,
        "Failed to fetch aliases",
      );
      set({ aliases: data, isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to fetch aliases",
        isLoading: false,
      });
      throw error;
    }
  },

  createAlias: async (data) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/aliases", {
        method: "POST",
        body: JSON.stringify(data),
      });

      await expectSuccess(response, "Failed to create alias");

      // Refresh the list
      await get().fetchAliases();
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to create alias",
        isLoading: false,
      });
      throw error;
    }
  },

  updateAlias: async (id: number, data) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/aliases/${id}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      });

      await expectSuccess(response, "Failed to update alias");

      // Refresh the list
      await get().fetchAliases();
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to update alias",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteAlias: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/aliases/${id}`, {
        method: "DELETE",
      });

      await expectSuccess(response, "Failed to delete alias");

      // Refresh the list
      await get().fetchAliases();
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to delete alias",
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
