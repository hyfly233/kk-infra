import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/pipeline': {
        target: 'http://127.0.0.1:8086',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/pipeline/, '/api'),
      },
      // 控制面 API：模型服务/GPU 资源/部署
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
      // 模型注册中心（模型/版本管理）
      '/model-registry': {
        target: 'http://127.0.0.1:8081',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/model-registry/, '/api'),
      },
      // 网关（API Key 管理）
      '/gateway': {
        target: 'http://127.0.0.1:8083',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/gateway/, '/api'),
      },
    },
  },
})
