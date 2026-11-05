import axios, { AxiosError } from 'axios'
import router from '../router'

// 所有请求带 X-Requested-With：授权后台据此做 CSRF 防护
export const http = axios.create({ baseURL: '/api/v1', headers: { 'X-Requested-With': 'etos' }, timeout: 30000 })

http.interceptors.response.use(
  (r) => r,
  (err: AxiosError<{ message?: string }>) => {
    const name = router.currentRoute.value.name
    if (err.response?.status === 401 && name !== 'login') {
      router.push({ name: 'login', query: { next: router.currentRoute.value.fullPath } })
    }
    return Promise.reject(err)
  },
)

export function errMsg(e: unknown): string {
  const ax = e as AxiosError<{ message?: string }>
  return ax.response?.data?.message || ax.message || String(e)
}

export const api = {
  get: <T = any>(url: string, params?: any) => http.get<T>(url, { params }).then((r) => r.data),
  post: <T = any>(url: string, body?: any) => http.post<T>(url, body).then((r) => r.data),
  put: <T = any>(url: string, body?: any) => http.put<T>(url, body).then((r) => r.data),
  del: <T = any>(url: string) => http.delete<T>(url).then((r) => r.data),
}

// download 以附件方式保存接口返回的文件
export async function download(url: string, fallback: string) {
  const r = await http.get(url, { responseType: 'blob' })
  const cd = String(r.headers['content-disposition'] || '')
  const m = /filename="([^"]+)"/.exec(cd)
  const a = document.createElement('a')
  a.href = URL.createObjectURL(r.data)
  a.download = m?.[1] || fallback
  a.click()
  URL.revokeObjectURL(a.href)
}

// readFile 读取用户选择的文件内容
export function readFile(): Promise<string> {
  return new Promise((resolve, reject) => {
    const el = document.createElement('input')
    el.type = 'file'
    el.accept = '.json,.lic,.txt'
    el.onchange = () => {
      const f = el.files?.[0]
      if (!f) return reject(new Error('未选择文件'))
      f.text().then(resolve, reject)
    }
    el.click()
  })
}
