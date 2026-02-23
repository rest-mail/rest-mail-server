import { create } from 'zustand'
import { apiV1 } from '../api'

interface Certificate {
  id: number
  domain_id: number
  issuer: string
  not_before: string
  not_after: string
  auto_renew: boolean
  created_at: string
  updated_at: string
  domain?: {
    id: number
    name: string
  }
}

interface CertificateDetail extends Certificate {
  cert_pem: string
}

interface CertificateState {
  certificates: Certificate[]
  currentCertificate: CertificateDetail | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchCertificates: (accessToken: string) => Promise<void>
  fetchCertificate: (id: number, accessToken: string) => Promise<void>
  uploadCertificate: (
    data: {
      domain_id: number
      cert_pem: string
      key_pem: string
      auto_renew?: boolean
    },
    accessToken: string
  ) => Promise<void>
  deleteCertificate: (id: number, accessToken: string) => Promise<void>
  getExpiringCertificates: (days?: number) => Certificate[]
  clearError: () => void
}

export const useCertificateStore = create<CertificateState>((set, get) => ({
  certificates: [],
  currentCertificate: null,
  isLoading: false,
  error: null,

  fetchCertificates: async (accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request('/admin/certificates', { method: 'GET' }, accessToken)
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch certificates')
      }
      const response_data = await response.json()
      const data = response_data.data || response_data
      set({ certificates: data.items || data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch certificates',
        isLoading: false,
      })
      throw error
    }
  },

  fetchCertificate: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(`/admin/certificates/${id}`, { method: 'GET' }, accessToken)
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch certificate')
      }
      const response_data = await response.json()
      const data = response_data.data || response_data
      set({ currentCertificate: data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch certificate',
        isLoading: false,
      })
      throw error
    }
  },

  uploadCertificate: async (data, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        '/admin/certificates',
        {
          method: 'POST',
          body: JSON.stringify(data),
          headers: { 'Content-Type': 'application/json' },
        },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to upload certificate')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to upload certificate',
        isLoading: false,
      })
      throw error
    }
  },

  deleteCertificate: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(`/admin/certificates/${id}`, { method: 'DELETE' }, accessToken)
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete certificate')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete certificate',
        isLoading: false,
      })
      throw error
    }
  },

  getExpiringCertificates: (days = 30) => {
    const now = new Date()
    const threshold = new Date(now.getTime() + days * 24 * 60 * 60 * 1000)
    return get().certificates.filter((cert) => {
      const expiryDate = new Date(cert.not_after)
      return expiryDate <= threshold && expiryDate > now
    })
  },

  clearError: () => set({ error: null }),
}))
