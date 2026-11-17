<script setup>
import { ref, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { state, rpc } from '../store.js'
import Icon from '../components/Icon.vue'

const router = useRouter()
const route = useRoute()
const username = ref('')
const password = ref('')
const err = ref('')
const busy = ref(false)
const user = ref(null)
onMounted(() => user.value?.focus())

async function submit() {
  err.value = ''
  busy.value = true
  try {
    state.me = await rpc('auth.login', { username: username.value, password: password.value })
    state.serverName = state.me.serverName
    router.replace(route.query.next || '/')
  } catch (e) {
    err.value = e.message
    password.value = ''
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="auth-page">
    <form class="auth-box" @submit.prevent="submit">
      <div class="brand"><Icon name="play" :size="34" class="logo" /> {{ state.serverName }}</div>
      <h2>登录</h2>
      <div class="field">
        <label for="u">用户名</label>
        <input id="u" ref="user" v-model="username" class="input" autocomplete="username" required />
      </div>
      <div class="field">
        <label for="p">密码</label>
        <input id="p" v-model="password" class="input" type="password" autocomplete="current-password" required />
      </div>
      <div class="err">{{ err }}</div>
      <button class="btn primary big wide" type="submit" :disabled="busy">{{ busy ? '登录中…' : '登录' }}</button>
      <p class="hint sec"><Icon name="shield" :size="14" /> 端到端加密：{{ state.mode === 'local' ? '本地客户端模式' : '网页模式' }}</p>
    </form>
  </div>
</template>
