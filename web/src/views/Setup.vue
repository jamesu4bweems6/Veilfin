<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { state, rpc } from '../store.js'
import Icon from '../components/Icon.vue'

const router = useRouter()
const code = ref('')
const username = ref('admin')
const password = ref('')
const password2 = ref('')
const err = ref('')
const busy = ref(false)

async function submit() {
  err.value = ''
  if (password.value !== password2.value) {
    err.value = '两次输入的密码不一致'
    return
  }
  busy.value = true
  try {
    state.me = await rpc('setup.create', { code: code.value, username: username.value, password: password.value })
    state.needsSetup = false
    router.replace('/')
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="auth-page">
    <form class="auth-box" @submit.prevent="submit">
      <div class="brand"><Icon name="play" :size="34" class="logo" /> Veilfin</div>
      <h2>首次使用：创建管理员</h2>
      <p class="hint">初始化代码打印在服务端的启动日志里，用来防止别人抢先完成初始化。</p>
      <div class="field">
        <label for="c">初始化代码</label>
        <input id="c" v-model="code" class="input" autocomplete="off" required />
      </div>
      <div class="field">
        <label for="u">管理员用户名</label>
        <input id="u" v-model="username" class="input" autocomplete="username" required />
      </div>
      <div class="field">
        <label for="p">密码（至少 8 位）</label>
        <input id="p" v-model="password" class="input" type="password" autocomplete="new-password" minlength="8" required />
      </div>
      <div class="field">
        <label for="p2">确认密码</label>
        <input id="p2" v-model="password2" class="input" type="password" autocomplete="new-password" required />
      </div>
      <div class="err">{{ err }}</div>
      <button class="btn primary big wide" type="submit" :disabled="busy">创建并登录</button>
    </form>
  </div>
</template>
