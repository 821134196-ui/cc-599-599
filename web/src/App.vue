<script setup>
import { provide, ref } from 'vue'
import ReaderView from './views/ReaderView.vue'
import ClerkView from './views/ClerkView.vue'

const tab = ref('reader')
const toasts = ref([])
let seq = 0
function toast(msg, kind = 'ok') {
  const id = ++seq
  toasts.value.push({ id, msg, kind })
  setTimeout(() => {
    toasts.value = toasts.value.filter(t => t.id !== id)
  }, 3600)
}
provide('toast', toast)
</script>

<template>
  <header class="topbar">
    <h1>报刊亭期刊订阅 · 到刊与缺刊补发系统</h1>
    <p>逐期号记录履约：到刊、领取签收、缺刊补发、停刊退款，全程幂等可核对</p>
  </header>
  <nav class="tabs">
    <button :class="{ active: tab === 'reader' }" @click="tab = 'reader'">读者页面</button>
    <button :class="{ active: tab === 'clerk' }" @click="tab = 'clerk'">店员工作台</button>
  </nav>
  <main>
    <ReaderView v-if="tab === 'reader'" />
    <ClerkView v-else @toast="(m, k) => toast(m, k)" />
  </main>
  <div class="toast">
    <div v-for="t in toasts" :key="t.id" class="t" :class="t.kind">{{ t.msg }}</div>
  </div>
</template>
