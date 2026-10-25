import { create } from "zustand";
import { apiV1 } from "../api";
import type { JsonResponse } from "../api-contract";
import { expectSuccess } from "../api-contract";

export type QueueStatus = QueueEntry["status"];

type QueueEntry = JsonResponse<"getQueueEntry", 200>["data"];

interface QueueState {
  entries: QueueEntry[];
  currentEntry: QueueEntry | null;
  isLoading: boolean;
  error: string | null;
  filter: QueueStatus | "all";
  selectedIds: number[];

  // Actions
  fetchQueue: (filter?: QueueStatus | "all") => Promise<void>;
  fetchEntry: (id: number) => Promise<void>;
  retryEntry: (id: number) => Promise<void>;
  deleteEntry: (id: number) => Promise<void>;
  setFilter: (filter: QueueStatus | "all") => void;
  clearError: () => void;
  clearCurrentEntry: () => void;
  toggleSelection: (id: number) => void;
  selectAll: (ids: number[]) => void;
  clearSelection: () => void;
  retryBulk: (ids: number[]) => Promise<void>;
  deleteBulk: (ids: number[]) => Promise<void>;
}

export const useQueueStore = create<QueueState>((set, get) => ({
  entries: [],
  currentEntry: null,
  isLoading: false,
  error: null,
  filter: "all",
  selectedIds: [],

  fetchQueue: async (filter?: QueueStatus | "all") => {
    set({ isLoading: true, error: null });

    try {
      const filterParam = filter || get().filter;
      const path =
        filterParam === "all"
          ? "/admin/queue"
          : `/admin/queue?status=${filterParam}`;

      const response = await apiV1.request(path, { method: "GET" });

      await expectSuccess(response, "Failed to fetch queue entries");

      const response_data: JsonResponse<"listQueue", 200> =
        await response.json();
      const data = response_data.data;
      const entries = data.items;
      set({
        entries,
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch queue entries",
        isLoading: false,
      });
      throw error;
    }
  },

  fetchEntry: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/queue/${id}`, {
        method: "GET",
      });

      await expectSuccess(response, "Failed to fetch queue entry");

      const response_data: JsonResponse<"getQueueEntry", 200> =
        await response.json();
      const data = response_data.data;
      set({
        currentEntry: data,
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch queue entry",
        isLoading: false,
      });
      throw error;
    }
  },

  retryEntry: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/queue/${id}/retry`, {
        method: "POST",
      });

      await expectSuccess(response, "Failed to retry queue entry");

      // Refresh the queue list and current entry if viewing details
      const currentFilter = get().filter;
      await get().fetchQueue(currentFilter);

      if (get().currentEntry?.id === id) {
        await get().fetchEntry(id);
      }

      set({
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to retry queue entry",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteEntry: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/queue/${id}`, {
        method: "DELETE",
      });

      await expectSuccess(response, "Failed to delete queue entry");

      set({
        entries: get().entries.filter((e) => e.id !== id),
        currentEntry: get().currentEntry?.id === id ? null : get().currentEntry,
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to delete queue entry",
        isLoading: false,
      });
      throw error;
    }
  },

  setFilter: (filter: QueueStatus | "all") => {
    set({ filter });
  },

  clearError: () => {
    set({ error: null });
  },

  clearCurrentEntry: () => {
    set({ currentEntry: null });
  },

  toggleSelection: (id: number) => {
    set((state) => ({
      selectedIds: state.selectedIds.includes(id)
        ? state.selectedIds.filter((i) => i !== id)
        : [...state.selectedIds, id],
    }));
  },

  selectAll: (ids: number[]) => {
    set({ selectedIds: ids });
  },

  clearSelection: () => {
    set({ selectedIds: [] });
  },

  retryBulk: async (ids: number[]) => {
    set({ isLoading: true, error: null });

    try {
      // Retry each entry sequentially
      for (const id of ids) {
        await expectSuccess(
          await apiV1.request(`/admin/queue/${id}/retry`, { method: "POST" }),
          "Failed to retry queue entry",
        );
      }

      // Refresh queue and clear selection
      await get().fetchQueue(get().filter);
      set({ selectedIds: [], isLoading: false });
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Bulk retry failed",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteBulk: async (ids: number[]) => {
    set({ isLoading: true, error: null });

    try {
      // Delete each entry sequentially
      for (const id of ids) {
        await expectSuccess(
          await apiV1.request(`/admin/queue/${id}`, { method: "DELETE" }),
          "Failed to delete queue entry",
        );
      }

      // Refresh queue and clear selection
      await get().fetchQueue(get().filter);
      set({ selectedIds: [], isLoading: false });
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Bulk delete failed",
        isLoading: false,
      });
      throw error;
    }
  },
}));
