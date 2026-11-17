<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useIndicator } from '../composables/indicator.js'
import { state, rpc, lockNow, toastError, requestPin } from '../store.js'
import Icon from './Icon.vue'

const router = useRouter()
const route = useRoute()
const menu = ref(false)
const nav = ref(null)
const scrolled = ref(false)
const libs = ref([])
// 当前标签下方的胶囊高亮会在标签之间平滑滑动
const { style: pill } = useIndicator(nav, '.nav-link.active', () => [route.path, libs.value.length])

function onScroll() {
  scrolled.value = window.scrollY > 10
}
function closeMenu(e) {
  if (!e.target.closest?.('.user-menu')) menu.value = false
}
onMounted(async () => {
  window.addEventListener('scroll', onScroll, { passive: true })
  document.addEventListener('click', closeMenu)
  try {
    libs.value = await rpc('lib.list')
  } catch {}
})
onBeforeUnmount(() => {
  window.removeEventListener('scroll', onScroll)
  document.removeEventListener('click', closeMenu)
})

async function logout() {
  menu.value = false
  try {
    await rpc('auth.logout')
  } catch {}
  state.me = null
  router.push('/login')
}

async function toggleLock() {
  menu.value = false
  try {
    if (state.me.unlocked && state.me.hasPin) {
      await lockNow()
      router.push('/')
    } else {
      await requestPin()
    }
  } catch (e) {
    toastError(e)
  }
}
</script>

<template>
  <header class="topbar" :class="{ solid: scrolled }">
    <router-link to="/" class="brand"><Icon name="play" :size="22" class="logo" /> {{ state.serverName }}</router-link>
    <nav ref="nav" class="nav">
      <span class="indicator pill" :style="pill" />
      <router-link to="/" class="nav-link" exact-active-class="active">首页</router-link>
      <router-link v-for="l in libs" :key="l.id" :to="`/library/${l.id}`" class="nav-link lib-link" active-class="active">
        {{ l.name }}<Icon v-if="l.restricted" name="lock" :size="13" class="lk" />
      </router-link>
    </nav>
    <div class="spacer" />
    <router-link to="/search" class="icon-btn" title="搜索"><Icon name="search" /></router-link>
    <router-link to="/history" class="icon-btn" title="观看记录"><Icon name="history" /></router-link>
    <button v-if="state.me?.hasPin" class="icon-btn" :title="state.me.unlocked ? '锁定受限内容' : '解锁受限内容'" @click="toggleLock">
      <Transition name="swap" mode="out-in">
        <Icon :key="state.me.unlocked ? 'u' : 'l'" :name="state.me.unlocked ? 'unlock' : 'lock'" :class="{ on: state.me.unlocked }" />
      </Transition>
    </button>
    <div class="user-menu">
      <button class="avatar" :title="state.me?.username" @click="menu = !menu">{{ (state.me?.username || '?').slice(0, 1).toUpperCase() }}</button>
      <Transition name="pop">
        <div v-if="menu" class="menu" @click="menu = false">
          <div class="who muted">{{ state.me.username }}<span v-if="state.me.isAdmin"> · 管理员</span></div>
          <hr />
          <router-link to="/settings"><Icon name="settings" :size="18" /> 设置</router-link>
          <router-link v-if="state.me.isAdmin" to="/admin"><Icon name="admin" :size="18" /> 管理后台</router-link>
          <hr />
          <button @click="logout"><Icon name="logout" :size="18" /> 退出登录</button>
        </div>
      </Transition>
    </div>
  </header>
</template>

<style scoped>
/* 毛玻璃顶栏：滚动后背景渐变为半透明磨砂 */
.topbar { position: fixed; top: 0; left: 0; right: 0; z-index: 50; height: var(--topbar); display: flex; align-items: center; gap: 4px;
  padding: 0 3vw; background-color: transparent; background-image: linear-gradient(rgba(0,0,0,.7), rgba(0,0,0,0));
  transition: background-color .35s var(--ease-out), box-shadow .35s var(--ease-out), backdrop-filter .35s var(--ease-out); }
.topbar.solid { background-color: rgba(18,18,18,.72); background-image: none; backdrop-filter: saturate(180%) blur(20px);
  -webkit-backdrop-filter: saturate(180%) blur(20px); box-shadow: 0 0.5px 0 rgba(255,255,255,.08); }
.brand { display: flex; align-items: center; gap: 6px; font-size: 18px; font-weight: 600; margin-right: 22px; white-space: nowrap; transition: opacity .2s; }
.brand:hover { opacity: .85; }
.logo { color: var(--accent); }
.nav { position: relative; display: flex; gap: 2px; overflow-x: auto; scrollbar-width: none; min-width: 0; padding: 4px; }
.nav::-webkit-scrollbar { display: none; }
.nav-link { position: relative; z-index: 1; padding: 6px 14px; border-radius: 999px; color: #c8c8c8; white-space: nowrap; display: inline-flex; align-items: center; gap: 4px;
  transition: color .3s var(--ease-out), transform .35s var(--spring-bouncy); }
.nav-link:hover { color: #fff; }
.nav-link:active { transform: scale(.94); transition-duration: .08s; }
.nav-link.active { color: #fff; }
/* 滑动的胶囊高亮（类似 iOS 分段控件） */
.pill { left: 0; top: 0; border-radius: 999px; background: rgba(255,255,255,.12); box-shadow: inset 0 0 0 0.5px rgba(255,255,255,.12); }
.lk { opacity: .6; }
.on { color: var(--warn); }
.user-menu { position: relative; margin-left: 6px; }
.avatar { width: 34px; height: 34px; border-radius: 50%; background: var(--accent); color: #fff; font-weight: 700; }
.avatar:hover { box-shadow: 0 0 0 3px rgba(82,181,75,.35); }
.menu { right: 0; top: 46px; }
.who { padding: 6px 12px; font-size: 13px; }
.swap-enter-active { transition: opacity .2s, transform .4s var(--spring-bouncy); }
.swap-leave-active { transition: opacity .12s, transform .12s; }
.swap-enter-from { opacity: 0; transform: scale(.5) rotate(-30deg); }
.swap-leave-to { opacity: 0; transform: scale(.6); }
@media (max-width: 700px) { .lib-link { display: none; } .brand { margin-right: 8px; } }
</style>
