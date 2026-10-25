import { create } from "zustand";
import { apiV1 } from "../api";
import type { JsonResponse, QueryParameters } from "../api-contract";
import { expectSuccess } from "../api-contract";

// A rendering projection of an RFC 8460 failure row. The generated wire value
// stays unknown; the detail screen narrows these fields before rendering them.
export interface TLSFailureDetail {
  result_type?: string;
  receiving_mx_hostname?: string;
  receiving_ip?: string;
  failed_session_count?: number;
  [key: string]: unknown;
}

export type TLSReport = JsonResponse<"listTlsReports", 200>["data"][number];

interface TLSReportState {
  reports: TLSReport[];
  currentReport: TLSReport | null;
  isLoading: boolean;
  error: string | null;
  pagination: {
    total: number;
    hasMore: boolean;
  };

  // Actions
  fetchReports: (filters: QueryParameters<"listTlsReports">) => Promise<void>;
  selectReport: (report: TLSReport) => void;
  clearCurrentReport: () => void;
  clearError: () => void;
}

export const useTLSReportStore = create<TLSReportState>((set) => ({
  reports: [],
  currentReport: null,
  isLoading: false,
  error: null,
  pagination: { total: 0, hasMore: false },

  fetchReports: async (filters) => {
    set({ isLoading: true, error: null });
    try {
      const params = new URLSearchParams();
      if (filters.domain_id) params.set("domain_id", String(filters.domain_id));
      if (filters.policy_type) params.set("policy_type", filters.policy_type);
      if (filters.reporting_org)
        params.set("reporting_org", filters.reporting_org);
      if (filters.limit) params.set("limit", String(filters.limit));
      if (filters.offset) params.set("offset", String(filters.offset));

      const response = await apiV1.request(
        `/admin/tls-reports?${params.toString()}`,
        { method: "GET" },
      );
      await expectSuccess(response, "Failed to fetch TLS reports");
      const response_data: JsonResponse<"listTlsReports", 200> =
        await response.json();
      const data = response_data.data;
      set({
        reports: data,
        pagination: {
          total: response_data.pagination?.total || 0,
          hasMore: response_data.pagination?.has_more || false,
        },
        isLoading: false,
      });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch TLS reports",
        isLoading: false,
      });
      throw error;
    }
  },

  selectReport: (report: TLSReport) => set({ currentReport: report }),
  clearCurrentReport: () => set({ currentReport: null }),
  clearError: () => set({ error: null }),
}));
