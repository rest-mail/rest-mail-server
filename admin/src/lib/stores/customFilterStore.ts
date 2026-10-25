import { create } from "zustand";
import { apiV1 } from "../api";
import { expectSuccess } from "../api-contract";
import type { JsonResponse, RequestBody } from "../api-contract";
import { customFilterView } from "../pipeline-views";
import type { CustomFilter } from "../pipeline-views";

type ValidationResult = JsonResponse<"validateCustomFilter", 200>;

type FilterTestResult = JsonResponse<"testCustomFilter", 200>;

interface CustomFilterState {
  filters: CustomFilter[];
  currentFilter: CustomFilter | null;
  isLoading: boolean;
  error: string | null;

  // Actions
  fetchFilters: (domainId?: number) => Promise<void>;
  fetchFilter: (id: number) => Promise<CustomFilter>;
  createFilter: (
    data: RequestBody<"createCustomFilter">,
  ) => Promise<CustomFilter>;
  updateFilter: (
    id: number,
    data: RequestBody<"updateCustomFilter">,
  ) => Promise<CustomFilter>;
  deleteFilter: (id: number) => Promise<void>;
  validateScript: (
    script: string,
    email?: unknown,
  ) => Promise<ValidationResult>;
  testFilter: (id: number, email?: unknown) => Promise<FilterTestResult>;
  clearError: () => void;
  clearCurrentFilter: () => void;
}

export const useCustomFilterStore = create<CustomFilterState>((set, get) => ({
  filters: [],
  currentFilter: null,
  isLoading: false,
  error: null,

  fetchFilters: async (domainId?: number) => {
    set({ isLoading: true, error: null });

    try {
      const url = domainId
        ? `/admin/custom-filters?domain_id=${domainId}`
        : "/admin/custom-filters";

      const response = await apiV1.request(url, { method: "GET" });

      await expectSuccess(response, "Failed to fetch custom filters");

      const response_data: JsonResponse<"listCustomFilters", 200> =
        await response.json();
      const data = response_data.data;
      set({
        filters: data.map(customFilterView),
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch custom filters",
        isLoading: false,
      });
      throw error;
    }
  },

  fetchFilter: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/custom-filters/${id}`, {
        method: "GET",
      });

      await expectSuccess(response, "Failed to fetch custom filter");

      const response_data: JsonResponse<"getCustomFilter", 200> =
        await response.json();
      const data = customFilterView(response_data.data);
      set({
        currentFilter: data,
        isLoading: false,
        error: null,
      });
      return data;
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch custom filter",
        isLoading: false,
      });
      throw error;
    }
  },

  createFilter: async (data: RequestBody<"createCustomFilter">) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/custom-filters", {
        method: "POST",
        body: JSON.stringify(data),
      });

      await expectSuccess(response, "Failed to create custom filter");

      const response_data: JsonResponse<"createCustomFilter", 201> =
        await response.json();
      const filter = customFilterView(response_data.data);
      set({
        filters: [...get().filters, filter],
        isLoading: false,
        error: null,
      });
      return filter;
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to create custom filter",
        isLoading: false,
      });
      throw error;
    }
  },

  updateFilter: async (id: number, data: RequestBody<"updateCustomFilter">) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/custom-filters/${id}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      });

      await expectSuccess(response, "Failed to update custom filter");

      const response_data: JsonResponse<"updateCustomFilter", 200> =
        await response.json();
      const filter = customFilterView(response_data.data);
      set({
        filters: get().filters.map((f) => (f.id === id ? filter : f)),
        currentFilter: filter,
        isLoading: false,
        error: null,
      });
      return filter;
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to update custom filter",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteFilter: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/custom-filters/${id}`, {
        method: "DELETE",
      });

      await expectSuccess(response, "Failed to delete custom filter");

      set({
        filters: get().filters.filter((f) => f.id !== id),
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to delete custom filter",
        isLoading: false,
      });
      throw error;
    }
  },

  validateScript: async (script: string, email?: unknown) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/custom-filters/validate", {
        method: "POST",
        body: JSON.stringify({
          script,
          email,
        } satisfies RequestBody<"validateCustomFilter">),
      });

      await expectSuccess(response, "Failed to validate script");

      const result: JsonResponse<"validateCustomFilter", 200> =
        await response.json();
      set({ isLoading: false, error: null });
      return result;
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to validate script",
        isLoading: false,
      });
      throw error;
    }
  },

  testFilter: async (id: number, email?: unknown) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/custom-filters/${id}/test`, {
        method: "POST",
        body: JSON.stringify({
          email,
        } satisfies RequestBody<"testCustomFilter">),
      });

      await expectSuccess(response, "Failed to test custom filter");

      const result: JsonResponse<"testCustomFilter", 200> =
        await response.json();
      set({ isLoading: false, error: null });
      return result;
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to test custom filter",
        isLoading: false,
      });
      throw error;
    }
  },

  clearError: () => {
    set({ error: null });
  },

  clearCurrentFilter: () => {
    set({ currentFilter: null });
  },
}));

// Export types for use in components
export type { CustomFilter, ValidationResult, FilterTestResult };
