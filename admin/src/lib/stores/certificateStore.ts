import { create } from "zustand";
import { apiV1 } from "../api";
import type { JsonResponse, RequestBody } from "../api-contract";
import { expectSuccess } from "../api-contract";

type Certificate = JsonResponse<"listCertificates", 200>["data"][number];

type CertificateDetail = JsonResponse<"getCertificate", 200>["data"];

interface CertificateState {
  certificates: Certificate[];
  currentCertificate: CertificateDetail | null;
  isLoading: boolean;
  error: string | null;

  // Actions
  fetchCertificates: () => Promise<void>;
  fetchCertificate: (id: number) => Promise<void>;
  uploadCertificate: (data: RequestBody<"createCertificate">) => Promise<void>;
  deleteCertificate: (id: number) => Promise<void>;
  getExpiringCertificates: (days?: number) => Certificate[];
  clearError: () => void;
}

export const useCertificateStore = create<CertificateState>((set, get) => ({
  certificates: [],
  currentCertificate: null,
  isLoading: false,
  error: null,

  fetchCertificates: async () => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request("/admin/certificates", {
        method: "GET",
      });
      await expectSuccess(response, "Failed to fetch certificates");
      const response_data: JsonResponse<"listCertificates", 200> =
        await response.json();
      const data = response_data.data;
      set({ certificates: data, isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch certificates",
        isLoading: false,
      });
      throw error;
    }
  },

  fetchCertificate: async (id: number) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(`/admin/certificates/${id}`, {
        method: "GET",
      });
      await expectSuccess(response, "Failed to fetch certificate");
      const response_data: JsonResponse<"getCertificate", 200> =
        await response.json();
      const data = response_data.data;
      set({ currentCertificate: data, isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to fetch certificate",
        isLoading: false,
      });
      throw error;
    }
  },

  uploadCertificate: async (data) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request("/admin/certificates", {
        method: "POST",
        body: JSON.stringify(data),
        headers: { "Content-Type": "application/json" },
      });
      await expectSuccess(response, "Failed to upload certificate");
      set({ isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to upload certificate",
        isLoading: false,
      });
      throw error;
    }
  },

  deleteCertificate: async (id: number) => {
    set({ isLoading: true, error: null });
    try {
      const response = await apiV1.request(`/admin/certificates/${id}`, {
        method: "DELETE",
      });
      await expectSuccess(response, "Failed to delete certificate");
      set({ isLoading: false });
    } catch (error) {
      set({
        error:
          error instanceof Error
            ? error.message
            : "Failed to delete certificate",
        isLoading: false,
      });
      throw error;
    }
  },

  getExpiringCertificates: (days = 30) => {
    const now = new Date();
    const threshold = new Date(now.getTime() + days * 24 * 60 * 60 * 1000);
    return get().certificates.filter((cert) => {
      const expiryDate = new Date(cert.not_after);
      return expiryDate <= threshold && expiryDate > now;
    });
  },

  clearError: () => set({ error: null }),
}));
