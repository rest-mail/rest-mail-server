import { create } from "zustand";
import { apiV1 } from "../api";
import { expectSuccess } from "../api-contract";
import type {
  JsonResponse,
  RequestBody,
  QueryParameters,
} from "../api-contract";
import type { components } from "../api-types";
import { pipelineView, traceView } from "../pipeline-views";
import type {
  Pipeline,
  FilterConfig,
  TraceStage,
  MessageTrace,
} from "../pipeline-views";

type FilterLogStep = components["schemas"]["FilterLog"];

type PipelineTestResult = JsonResponse<"testPipeline", 200>["data"];

type FilterTestResult = JsonResponse<"testFilter", 200>["data"];

type TraceQueryParams = QueryParameters<"listPipelineLogs">;

interface PipelineState {
  pipelines: Pipeline[];
  currentPipeline: Pipeline | null;
  traces: MessageTrace[];
  currentTrace: MessageTrace | null;
  isLoading: boolean;
  error: string | null;

  // Actions
  fetchPipelines: (domainId?: number) => Promise<void>;
  fetchPipeline: (id: number) => Promise<Pipeline>;
  createPipeline: (data: RequestBody<"createPipeline">) => Promise<Pipeline>;
  updatePipeline: (
    id: number,
    data: RequestBody<"updatePipeline">,
  ) => Promise<Pipeline>;
  deletePipeline: (id: number) => Promise<void>;
  testPipeline: (
    pipelineId: number,
    email: RequestBody<"testPipeline">["email"],
  ) => Promise<PipelineTestResult>;
  testFilter: (
    filterName: string,
    config: unknown,
    email: RequestBody<"testPipeline">["email"],
  ) => Promise<FilterTestResult>;
  fetchTraces: (params: TraceQueryParams) => Promise<void>;
  fetchMessageTrace: (id: number) => Promise<MessageTrace>;
  clearError: () => void;
  clearCurrentPipeline: () => void;
  clearCurrentTrace: () => void;
}

export const usePipelineStore = create<PipelineState>((set, get) => ({
  pipelines: [],
  currentPipeline: null,
  traces: [],
  currentTrace: null,
  isLoading: false,
  error: null,

  fetchPipelines: async (domainId?: number) => {
    set({ isLoading: true, error: null });

    try {
      const url = domainId
        ? `/admin/pipelines?domain_id=${domainId}`
        : "/admin/pipelines";

      const response = await apiV1.request(url, { method: "GET" });

      await expectSuccess(response, "Failed to fetch pipelines");

      const response_data: JsonResponse<"listPipelines", 200> =
        await response.json();
      const data = response_data.data;
      set({
        pipelines: data.map(pipelineView),
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to fetch pipelines",
        isLoading: false,
      });
      throw error;
    }
  },

  fetchPipeline: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      let offset = 0;
      let data: Pipeline | undefined;
      while (data === undefined) {
        const response = await apiV1.request(
          `/admin/pipelines?limit=200&offset=${offset}`,
          { method: "GET" },
        );
        await expectSuccess(response, "Failed to fetch pipeline");
        const page: JsonResponse<"listPipelines", 200> = await response.json();
        const found = page.data.find((pipeline) => pipeline.id === id);
        if (found) {
          data = pipelineView(found);
          break;
        }
        if (!page.pagination?.has_more) throw new Error("Pipeline not found");
        const nextOffset = offset + page.data.length;
        if (nextOffset <= offset)
          throw new Error("The pipeline list did not advance");
        offset = nextOffset;
      }
      set({
        currentPipeline: data,
        isLoading: false,
        error: null,
      });
      return data;
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to fetch pipeline",
        isLoading: false,
      });
      throw error;
    }
  },

  createPipeline: async (data: RequestBody<"createPipeline">) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/pipelines", {
        method: "POST",
        body: JSON.stringify(data),
      });

      await expectSuccess(response, "Failed to create pipeline");

      const response_data: JsonResponse<"createPipeline", 201> =
        await response.json();
      const pipeline = pipelineView(response_data.data);
      set({
        pipelines: [...get().pipelines, pipeline],
        isLoading: false,
        error: null,
      });
      return pipeline;
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to create pipeline",
        isLoading: false,
      });
      throw error;
    }
  },

  updatePipeline: async (id: number, data: RequestBody<"updatePipeline">) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/pipelines/${id}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      });

      await expectSuccess(response, "Failed to update pipeline");

      const response_data: JsonResponse<"updatePipeline", 200> =
        await response.json();
      const pipeline = pipelineView(response_data.data);
      set({
        pipelines: get().pipelines.map((p) => (p.id === id ? pipeline : p)),
        currentPipeline: pipeline,
        isLoading: false,
        error: null,
      });
      return pipeline;
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to update pipeline",
        isLoading: false,
      });
      throw error;
    }
  },

  deletePipeline: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/pipelines/${id}`, {
        method: "DELETE",
      });

      await expectSuccess(response, "Failed to delete pipeline");

      set({
        pipelines: get().pipelines.filter((p) => p.id !== id),
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to delete pipeline",
        isLoading: false,
      });
      throw error;
    }
  },

  testPipeline: async (
    pipelineId: number,
    email: RequestBody<"testPipeline">["email"],
  ) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/pipelines/test", {
        method: "POST",
        body: JSON.stringify({
          pipeline_id: pipelineId,
          email,
        } satisfies RequestBody<"testPipeline">),
      });

      await expectSuccess(response, "Failed to test pipeline");

      const { data: result }: JsonResponse<"testPipeline", 200> =
        await response.json();
      set({ isLoading: false, error: null });
      return result;
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to test pipeline",
        isLoading: false,
      });
      throw error;
    }
  },

  testFilter: async (
    filterName: string,
    config: unknown,
    email: RequestBody<"testPipeline">["email"],
  ) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request("/admin/pipelines/test-filter", {
        method: "POST",
        body: JSON.stringify({
          filter_name: filterName,
          config,
          email,
        } satisfies RequestBody<"testFilter">),
      });

      await expectSuccess(response, "Failed to test filter");

      const { data: result }: JsonResponse<"testFilter", 200> =
        await response.json();
      set({ isLoading: false, error: null });
      return result;
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : "Failed to test filter",
        isLoading: false,
      });
      throw error;
    }
  },

  // fetchTraces lists per-message traces from the repointed pipelines/logs
  // endpoint (now backed by message_traces), newest first, with the richer
  // outcome/reason_code/rfc_message_id filters.
  fetchTraces: async (params: TraceQueryParams) => {
    set({ isLoading: true, error: null });

    try {
      const queryParams = new URLSearchParams();
      if (params.pipeline_id)
        queryParams.set("pipeline_id", params.pipeline_id.toString());
      if (params.direction) queryParams.set("direction", params.direction);
      if (params.action) queryParams.set("action", params.action);
      if (params.outcome) queryParams.set("outcome", params.outcome);
      if (params.reason_code)
        queryParams.set("reason_code", params.reason_code);
      if (params.rfc_message_id)
        queryParams.set("rfc_message_id", params.rfc_message_id);
      if (params.limit) queryParams.set("limit", params.limit.toString());
      if (params.offset) queryParams.set("offset", params.offset.toString());

      const url = `/admin/pipelines/logs${queryParams.toString() ? "?" + queryParams.toString() : ""}`;
      const response = await apiV1.request(url, { method: "GET" });

      await expectSuccess(response, "Failed to fetch traces");

      const response_data: JsonResponse<"listPipelineLogs", 200> =
        await response.json();
      const data = response_data.data;
      set({
        traces: data.map(traceView),
        isLoading: false,
        error: null,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error ? error.message : "Failed to fetch traces",
        isLoading: false,
      });
      throw error;
    }
  },

  // fetchMessageTrace loads the single delivered-message trace timeline from
  // GET /admin/messages/{id}/trace (keyed on the delivered messages row id).
  fetchMessageTrace: async (id: number) => {
    set({ isLoading: true, error: null });

    try {
      const response = await apiV1.request(`/admin/messages/${id}/trace`, {
        method: "GET",
      });

      await expectSuccess(response, "Failed to fetch message trace");

      const response_data: JsonResponse<"getMessageTrace", 200> =
        await response.json();
      const data = traceView(response_data.data);
      set({ currentTrace: data, isLoading: false, error: null });
      return data;
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch message trace",
        isLoading: false,
      });
      throw error;
    }
  },

  clearError: () => {
    set({ error: null });
  },

  clearCurrentPipeline: () => {
    set({ currentPipeline: null });
  },

  clearCurrentTrace: () => {
    set({ currentTrace: null });
  },
}));

// Export types for use in components
export type {
  Pipeline,
  FilterConfig,
  MessageTrace,
  TraceStage,
  FilterLogStep,
  PipelineTestResult,
  FilterTestResult,
  TraceQueryParams,
};
