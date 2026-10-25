import { create } from "zustand";
import { apiV1 } from "../api";
import type { JsonResponse, RequestBody } from "../api-contract";
import { expectSuccess } from "../api-contract";

type MTASTSPolicy = JsonResponse<"getMtaStsPolicy", 200>["data"];

interface MTASTSState {
  policies: Map<number, MTASTSPolicy>;
  currentPolicy: MTASTSPolicy | null;
  isLoading: boolean;
  error: string | null;

  // Actions
  fetchPolicy: (domainId: number) => Promise<void>;
  setPolicy: (
    domainId: number,
    data: RequestBody<"setMtaStsPolicy">,
  ) => Promise<void>;
  deletePolicy: (domainId: number) => Promise<void>;
  clearCurrentPolicy: () => void;
  clearError: () => void;
}

export const useMTASTSStore = create<MTASTSState>((set, get) => ({
  policies: new Map(),
  currentPolicy: null,
  isLoading: false,
  error: null,

  fetchPolicy: async (domainId: number) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        { method: "GET" },
      );
      if (!response.ok) {
        if (response.status === 404) {
          // No policy exists for this domain
          set({ currentPolicy: null, isLoading: false });
          return;
        }
        await expectSuccess(response, "Failed to fetch MTA-STS policy");
      }
      const response_data: JsonResponse<"getMtaStsPolicy", 200> =
        await response.json();
      const data = response_data.data;
      const policies = new Map(get().policies);
      policies.set(domainId, data);
      set({ policies, currentPolicy: data, isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch MTA-STS policy",
        isLoading: false,
      });
      throw error;
    }
  },

  setPolicy: async (domainId: number, data) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        {
          method: "PUT",
          body: JSON.stringify(data),
          headers: { "Content-Type": "application/json" },
        },
      );
      await expectSuccess(response, "Failed to set MTA-STS policy");
      const { data: result }: JsonResponse<"setMtaStsPolicy", 200> =
        await response.json();
      const policies = new Map(get().policies);
      policies.set(domainId, result);
      set({ policies, currentPolicy: result, isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to set MTA-STS policy",
        isLoading: false,
      });
      throw error;
    }
  },

  deletePolicy: async (domainId: number) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        { method: "DELETE" },
      );
      await expectSuccess(response, "Failed to delete MTA-STS policy");
      const policies = new Map(get().policies);
      policies.delete(domainId);
      set({ policies, currentPolicy: null, isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to delete MTA-STS policy",
        isLoading: false,
      });
      throw error;
    }
  },

  clearCurrentPolicy: () => set({ currentPolicy: null }),
  clearError: () => set({ error: null }),
}));
