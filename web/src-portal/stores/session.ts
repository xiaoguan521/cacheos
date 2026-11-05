import { defineStore } from 'pinia'
import { api } from '../api/client'

export interface Me {
  id: number
  username: string
  display_name: string
  roles: string[]
}

export const roleName: Record<string, string> = { admin: '管理员', sales: '销售', business: '商务', signer: '签名员' }

export const statusName: Record<string, string> = {
  draft: '草稿',
  submitted: '待审批',
  approved: '已审批',
  rejected: '已驳回',
  batched: '待离线签名',
  issued: '已签发',
  revoked: '已吊销',
}

export const statusColor: Record<string, string> = {
  draft: 'default',
  submitted: 'gold',
  approved: 'cyan',
  rejected: 'red',
  batched: 'purple',
  issued: 'green',
  revoked: 'default',
}

export const editionName: Record<string, string> = { trial: '试用版', standard: '标准版', professional: '专业版', enterprise: '企业版' }

export const useSession = defineStore('session', {
  state: () => ({ me: null as Me | null }),
  actions: {
    async load() {
      this.me = await api.get<Me>('/auth/me')
    },
    async logout() {
      await api.post('/auth/logout').catch(() => {})
      this.me = null
    },
    has(role: string) {
      return !!this.me && (this.me.roles.includes(role) || this.me.roles.includes('admin'))
    },
  },
})
