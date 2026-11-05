import axios, { AxiosError } from 'axios'
import { message } from 'antdv-next'
import router from '../router'

// 所有请求带 X-Requested-With：控制台据此做 CSRF 防护
export const http = axios.create({
  baseURL: '/api/v1',
  headers: { 'X-Requested-With': 'etos' },
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
  put: <T = any>(url: string, body?: any) => http.put<T>(url, body).then((r) => r.data),
  del: <T = any>(url: string) => http.delete<T>(url).then((r) => r.data),
}
