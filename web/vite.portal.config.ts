import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 厂商授权后台前端（规格书 8.8）：与控制台共用依赖，单独构建到 web/portal/dist，
// 只由 CacheOS 授权服务内嵌，不进入客户的控制台程序。
export default defineConfig({
  root: 'src-portal',
  plugins: [vue()],
  build: { outDir: '../portal/dist', emptyOutDir: false, chunkSizeWarningLimit: 2500 },
  server: { proxy: { '/api': 'http://127.0.0.1:8600' } },
})
