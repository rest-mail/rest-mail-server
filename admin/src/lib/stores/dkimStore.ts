import { create } from "zustand";
import { apiV1 } from "../api";
import type { JsonResponse, RequestBody } from "../api-contract";
import { expectSuccess } from "../api-contract";

type DkimEntry = JsonResponse<"listDKIMKeys", 200>["data"][number];

interface DkimState {
  entries: DkimEntry[];
  currentEntry: DkimEntry | null;
  isLoading: boolean;
  error: string | null;

  // Actions
  fetchDkimKeys: () => Promise<void>;
  setDkimKey: (
    domainId: number,
    data: RequestBody<"setDKIMKey">,
  ) => Promise<void>;
  deleteDkimKey: (domainId: number) => Promise<void>;
  clearError: () => void;
}

export const useDkimStore = create<DkimState>((set) => ({
  entries: [],
  currentEntry: null,
  isLoading: false,
  error: null,

  fetchDkimKeys: async () => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request("/admin/dkim", { method: "GET" });
      await expectSuccess(response, "Failed to fetch DKIM keys");
      const response_data: JsonResponse<"listDKIMKeys", 200> =
        await response.json();
      const data = response_data.data;
      set({ entries: data, isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to fetch DKIM keys",
        isLoading: false,
      });
      throw error;
    }
  },

  setDkimKey: async (domainId: number, data: RequestBody<"setDKIMKey">) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(`/admin/dkim/${domainId}`, {
        method: "PUT",
        body: JSON.stringify(data),
        headers: { "Content-Type": "application/json" },
      });
      await expectSuccess(response, "Failed to set DKIM key");
      set({ isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to set DKIM key",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteDkimKey: async (domainId: number) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(`/admin/dkim/${domainId}`, {
        method: "DELETE",
      });
      await expectSuccess(response, "Failed to delete DKIM key");
      set({ isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to delete DKIM key",
        isLoading: false,
      });
      throw error;
    }
  },

  clearError: () => set({ error: null }),
}));
