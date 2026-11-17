<script setup>
import { ref } from 'vue'
import { useRoute } from 'vue-router'
import { useIndicator } from '../../composables/indicator.js'

const route = useRoute()
const tabs = ref(null)
const { style } = useIndicator(tabs, 'a.on', () => route.path)
</script>

<template>
  <div class="page admin">
    <h1 class="page-title">管理后台</h1>
    <nav ref="tabs" class="tabs">
      <span class="indicator underline" :style="style" />
      <router-link to="/admin" exact-active-class="on">概览</router-link>
      <router-link to="/admin/libraries" active-class="on">媒体库</router-link>
      <router-link to="/admin/users" active-class="on">用户</router-link>
      <router-link to="/admin/server" active-class="on">服务器设置</router-link>
    </nav>
    <router-view v-slot="{ Component, route: r }">
      <Transition name="page-tab" type="transition" mode="out-in">
        <component :is="Component" :key="r.path" />
      </Transition>
    </router-view>
  </div>
</template>

<style scoped>
.page-title { margin-bottom: 10px; }
.tabs { position: relative; display: flex; gap: 4px; border-bottom: 1px solid var(--line); overflow-x: auto; margin-bottom: 18px; scrollbar-width: none; }
.tabs a { position: relative; z-index: 1; padding: 9px 16px; color: var(--muted); white-space: nowrap; transition: color 0.25s; }
.tabs a:hover { color: var(--text); }
.tabs a.on { color: var(--text); }
/* 下划线指示器：只取高度的底部 2px */
.underline { left: 0; top: 0; }
.underline::after { content: ''; position: absolute; left: 10px; right: 10px; bottom: 0; height: 2px; border-radius: 2px; background: var(--accent); }
</style>
