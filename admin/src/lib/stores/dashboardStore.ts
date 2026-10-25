import { create } from "zustand";
import { apiV1 } from "../api";
import type { JsonResponse } from "../api-contract";
import { expectSuccess } from "../api-contract";

type DashboardStats = JsonResponse<"getDashboardStats", 200>["data"];

interface DashboardState {
  stats: DashboardStats | null;
  isLoading: boolean;
  error: string | null;

  // Actions
  fetchDashboardStats: () => Promise<void>;
  clearError: () => void;
}

export const useDashboardStore = create<DashboardState>()((set) => ({
  stats: null,
  isLoading: false,
  error: null,

  fetchDashboardStats: async () => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/stats", { method: "GET" });

      await expectSuccess(response, "Failed to fetch dashboard stats");

      const response_data: JsonResponse<"getDashboardStats", 200> =
        await response.json();
      const data = response_data.data;

      set({
        stats: data,
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch dashboard stats",
        isLoading: false,
      });
      throw error;
    }
  },

  clearError: () => {
    set({ error: null });
  },
}));
