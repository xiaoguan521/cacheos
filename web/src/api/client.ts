import axios, { AxiosError } from 'axios'
import { message } from 'antdv-next'
import router from '../router'

// 所有请求带 X-Requested-With：控制台据此做 CSRF 防护
export const http = axios.create({
  baseURL: '/api/v1',
  headers: { 'X-Requested-With': 'cacheos' },
  timeout: 30000,
})

export interface ApiError {
  code: string
  message: string
}

http.interceptors.response.use(
  (r) => r,
  (err: AxiosError<ApiError>) => {
    const status = err.response?.status
    const data = err.response?.data
    const cfg: any = err.config
    if (status === 401 && (data?.code === 'TOTP_STEP_UP_REQUIRED' || data?.code === 'TOTP_STEP_UP_INVALID') && cfg && !cfg._totpRetried && typeof window !== 'undefined') {
      const code = window.prompt('请输入新的身份验证器验证码（6 位）')?.trim()
      if (code) {
        cfg._totpRetried = true
        cfg.headers = cfg.headers || {}
        cfg.headers['X-TOTP-Code'] = code
        return http.request(cfg)
      }
    }
    if (status === 401 && router.currentRoute.value.name !== 'login' && router.currentRoute.value.name !== 'setup') {
      router.push({ name: 'login', query: { next: router.currentRoute.value.fullPath } })
    } else if (status === 403 && data?.code === 'PASSWORD_CHANGE_REQUIRED') {
      router.push({ name: 'password' })
    } else if (status === 423) {
      message.warning(data?.message || '授权已失效，控制台为只读状态')
    } else if (status === 402) {
      message.warning(data?.message || '当前授权不包含此功能')
    }
    return Promise.reject(err)
  },
)

export function errMsg(e: unknown): string {
  const ax = e as AxiosError<ApiError>
  return ax.response?.data?.message || ax.message || String(e)
}

export const api = {
  get: <T = any>(url: string, params?: any) => http.get<T>(url, { params }).then((r) => r.data),
  post: <T = any>(url: string, body?: any) => http.post<T>(url, body).then((r) => r.data),
  upload: <T = any>(url: string, body: FormData) => http.post<T>(url, body).then((r) => r.data),
  put: <T = any>(url: string, body?: any) => http.put<T>(url, body).then((r) => r.data),
  del: <T = any>(url: string) => http.delete<T>(url).then((r) => r.data),
}
