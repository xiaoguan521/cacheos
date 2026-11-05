import { defineStore } from 'pinia'
import { api } from '../api/client'

export interface LicenseView {
  state: 'UNLICENSED' | 'VALID' | 'EXPIRING' | 'GRACE' | 'EXPIRED' | 'INVALID'
  reason: string
  writable: boolean
  license_id?: string
  customer?: string
  edition?: string
  features?: string[]
  nodes_limit: number
  sites_limit: number
  expires_at?: string
  maintenance_until?: string
  days_left: number
  installation_id: string
  nodes_used: number
  sites_used: number
  crl?: { serial: number; issued_at: string; revoked: number } | null
}

const rank: Record<string, number> = { viewer: 1, releaser: 2, operator: 3, admin: 4 }

export const useSession = defineStore('session', {
  state: () => ({
    username: '' as string,
    role: '' as string,
    license: null as LicenseView | null,
  }),
  getters: {
    can: (s) => (min: string) => (rank[s.role] || 0) >= (rank[min] || 99),
    writable: (s) => !!s.license?.writable,
  },
  actions: {
    async loadMe() {
      const me = await api.get('/auth/me')
      this.username = me.username
      this.role = me.role
    },
    async loadLicense() {
      this.license = await api.get<LicenseView>('/license')
    },
    async logout() {
      await api.post('/auth/logout').catch(() => {})
      this.username = ''
      this.role = ''
    },
  },
})

export const editionName: Record<string, string> = {
  trial: '试用版',
  standard: '标准版',
  professional: '专业版',
  enterprise: '企业版',
}

export const licenseStateName: Record<string, string> = {
  UNLICENSED: '未授权',
  VALID: '有效',
  EXPIRING: '即将到期',
  GRACE: '宽限期',
  EXPIRED: '已过期',
  INVALID: '无效',
}
