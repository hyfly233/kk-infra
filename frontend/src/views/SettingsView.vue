<script setup lang="ts">
// 系统设置
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2>系统设置</h2>
    </div>
    <div class="panel">
      <div class="panel-title">平台能力</div>
	  <p class="dim">
		已启用租户隔离、GPU 配额、审计、用量账本与可选 KEDA 自动扩缩容。生产环境的 JWT 密钥、PostgreSQL、Prometheus 与集群依赖通过服务启动参数配置。
	  </p>
    </div>
    <div class="panel mt-16">
      <div class="panel-title">关于</div>
      <p class="dim">
        Carrot AI Infra Platform · 版本 0.1.0 (MVP)<br />
        基于 Kubernetes + vLLM 的 AI 推理服务平台
      </p>
    </div>
  </div>
</template>

<style scoped>
.dim {
  color: var(--text-dim);
  line-height: 1.8;
}
</style>
