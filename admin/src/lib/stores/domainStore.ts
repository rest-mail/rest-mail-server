import { create } from "zustand";
import { apiV1 } from "../api";
import { expectSuccess, readJSON } from "../api-contract";
import type { JsonResponse, RequestBody } from "../api-contract";

type Domain = JsonResponse<"getDomain", 200>["data"];
export type CreateDomainBody = RequestBody<"createDomain">;
type UpdateDomainBody = RequestBody<"updateDomain">;

interface DomainState {
  domains: Domain[];
  currentDomain: Domain | null;
  isLoading: boolean;
  error: string | null;

  // Actions
  fetchDomains: () => Promise<void>;
  fetchDomain: (id: string) => Promise<void>;
  createDomain: (domain: CreateDomainBody) => Promise<void>;
  updateDomain: (id: string, domain: UpdateDomainBody) => Promise<void>;
  deleteDomain: (id: string) => Promise<void>;
  clearError: () => void;
  clearCurrentDomain: () => void;
}

export const useDomainStore = create<DomainState>((set, get) => ({
  domains: [],
  currentDomain: null,
  isLoading: false,
  error: null,

  fetchDomains: async () => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/domains", { method: "GET" });

      const { data } = await readJSON<JsonResponse<"listDomains", 200>>(
        response,
        "Failed to fetch domains",
      );
      set({
        domains: data,
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to fetch domains",
        isLoading: false,
      });
      throw error;
    }
  },

  fetchDomain: async (id: string) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/domains/${id}`, {
        method: "GET",
      });

      const { data } = await readJSON<JsonResponse<"getDomain", 200>>(
        response,
        "Failed to fetch domain",
      );
      set({
        currentDomain: data,
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to fetch domain",
        isLoading: false,
      });
      throw error;
    }
  },

  createDomain: async (domain) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/domains", {
        method: "POST",
        body: JSON.stringify(domain),
      });

      const { data } = await readJSON<JsonResponse<"createDomain", 201>>(
        response,
        "Failed to create domain",
      );
      set({
        domains: [...get().domains, data],
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to create domain",
        isLoading: false,
      });
      throw error;
    }
  },

  updateDomain: async (id, domain) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/domains/${id}`, {
        method: "PATCH",
        body: JSON.stringify(domain),
      });

      const { data } = await readJSON<JsonResponse<"updateDomain", 200>>(
        response,
        "Failed to update domain",
      );
      set({
        domains: get().domains.map((d) => (d.id === parseInt(id) ? data : d)),
        currentDomain: data,
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to update domain",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteDomain: async (id: string) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/domains/${id}`, {
        method: "DELETE",
      });

      await expectSuccess(response, "Failed to delete domain");

      set({
        domains: get().domains.filter((d) => d.id !== parseInt(id)),
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to delete domain",
        isLoading: false,
      });
      throw error;
    }
  },

  clearError: () => {
    set({ error: null });
  },

  clearCurrentDomain: () => {
    set({ currentDomain: null });
  },
}));
