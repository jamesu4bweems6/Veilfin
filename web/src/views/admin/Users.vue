<script setup>
import { ref, onMounted } from 'vue'
import { state, rpc, toast, toastError } from '../../store.js'
import { fmtDateTime } from '../../util.js'
import Modal from '../../components/Modal.vue'

const users = ref([])
const libs = ref([])
const edit = ref(null) // 正在编辑的用户（新建时 id 为 0）
const del = ref(null)
const busy = ref(false)

async function load() {
  try {
    ;[users.value, libs.value] = await Promise.all([rpc('admin.users.list'), rpc('admin.libs.list')])
  } catch (e) {
    toastError(e)
  }
}
onMounted(load)

function openNew() {
  edit.value = { id: 0, username: '', password: '', isAdmin: false, disabled: false, clearPin: false, libraries: libs.value.filter((l) => !l.restricted).map((l) => l.id) }
}
function openEdit(u) {
  edit.value = { id: u.id, username: u.username, password: '', isAdmin: u.isAdmin, disabled: u.disabled, clearPin: false, hasPin: u.hasPin, libraries: [...u.libraries] }
}

async function save() {
  const u = edit.value
  busy.value = true
  try {
    if (u.id) {
      await rpc('admin.users.update', { id: u.id, isAdmin: u.isAdmin, disabled: u.disabled, password: u.password, clearPin: u.clearPin, libraries: u.libraries })
    } else {
      await rpc('admin.users.create', { username: u.username, password: u.password, isAdmin: u.isAdmin, libraries: u.libraries })
    }
    toast('已保存', 'ok')
    edit.value = null
    load()
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

async function doDelete() {
  try {
    await rpc('admin.users.delete', { id: del.value.id })
    del.value = null
    load()
  } catch (e) {
    toastError(e)
  }
}

const libName = (id) => libs.value.find((l) => l.id === id)?.name || '#' + id
</script>

<template>
  <div class="admin-body">
    <div class="row head">
      <span class="muted">管理员可以访问全部媒体库；普通用户只能访问勾选的媒体库。</span>
      <div class="spacer" />
      <button class="btn primary" @click="openNew">添加用户</button>
    </div>
    <div class="card-panel">
      <table class="list">
        <thead><tr><th>用户名</th><th>角色</th><th>可访问的媒体库</th><th>PIN</th><th>最近登录</th><th></th></tr></thead>
        <tbody>
          <tr v-for="(u, i) in users" :key="u.id" class="fade" :style="{ '--i': i }">
            <td>{{ u.username }} <span v-if="u.id === state.me.id" class="muted">（你）</span></td>
            <td>
              <span v-if="u.disabled" class="err">已禁用</span>
              <span v-else>{{ u.isAdmin ? '管理员' : '普通用户' }}</span>
            </td>
            <td class="libs">{{ u.isAdmin ? '全部' : u.libraries.map(libName).join('、') || '无' }}</td>
            <td>{{ u.hasPin ? '已设置' : '—' }}</td>
            <td class="muted">{{ u.lastLoginAt ? fmtDateTime(u.lastLoginAt) : '从未' }}</td>
            <td class="ops">
              <button class="btn ghost" @click="openEdit(u)">编辑</button>
              <button v-if="u.id !== state.me.id" class="btn ghost err" @click="del = u">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <Modal v-if="edit" :title="edit.id ? `编辑用户：${edit.username}` : '添加用户'" @close="edit = null">
      <form @submit.prevent="save">
        <div v-if="!edit.id" class="field"><label>用户名</label><input v-model="edit.username" class="input" required /></div>
        <div class="field">
          <label>{{ edit.id ? '重置密码（留空则不修改）' : '密码（至少 8 位）' }}</label>
          <input v-model="edit.password" class="input" type="password" autocomplete="new-password" :required="!edit.id" />
        </div>
        <div class="field">
          <label class="check"><input v-model="edit.isAdmin" type="checkbox" /> 管理员</label>
          <label v-if="edit.id" class="check"><input v-model="edit.disabled" type="checkbox" /> 禁用账号</label>
          <label v-if="edit.hasPin" class="check"><input v-model="edit.clearPin" type="checkbox" /> 清除该用户的 PIN</label>
        </div>
        <div v-if="!edit.isAdmin" class="field">
          <label>可访问的媒体库</label>
          <label v-for="l in libs" :key="l.id" class="check">
            <input v-model="edit.libraries" type="checkbox" :value="l.id" /> {{ l.name }} <span v-if="l.restricted" class="muted">（受限）</span>
          </label>
          <span v-if="!libs.length" class="muted">还没有媒体库</span>
        </div>
        <button type="submit" class="hidden" />
      </form>
      <template #footer>
        <button class="btn ghost" @click="edit = null">取消</button>
        <button class="btn primary" :disabled="busy" @click="save">保存</button>
      </template>
    </Modal>

    <Modal v-if="del" title="删除用户" @close="del = null">
      <p>确定删除用户「{{ del.username }}」？其观看记录和进度也会一并删除。</p>
      <template #footer>
        <button class="btn ghost" @click="del = null">取消</button>
        <button class="btn danger" @click="doDelete">删除</button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.head { margin-bottom: 14px; }
.libs { max-width: 320px; }
.ops { text-align: right; white-space: nowrap; }
.field .check { margin-bottom: 4px; }
</style>
