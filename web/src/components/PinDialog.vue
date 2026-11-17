<script setup>
import { ref, onMounted, nextTick } from 'vue'
import { submitPin, cancelPin } from '../store.js'
import Modal from './Modal.vue'

const pin = ref('')
const err = ref('')
const busy = ref(false)
const input = ref(null)
const shaking = ref(false)
onMounted(() => nextTick(() => input.value?.focus()))

async function submit() {
  if (!pin.value) return
  busy.value = true
  err.value = ''
  try {
    await submitPin(pin.value)
  } catch (e) {
    err.value = e.message
    pin.value = ''
    // 像 iOS 锁屏一样左右抖动
    shaking.value = false
    requestAnimationFrame(() => (shaking.value = true))
    input.value?.focus()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal title="解锁受限内容" width="360px" @close="cancelPin">
    <form :class="{ shake: shaking }" @submit.prevent="submit" @animationend="shaking = false">
      <p class="muted">请输入 PIN。解锁后 2 小时内有效，也可以随时在顶栏手动锁定。</p>
      <input ref="input" v-model="pin" class="input pin" type="password" inputmode="numeric" autocomplete="off" maxlength="8" placeholder="••••" />
      <Transition name="fade"><div v-if="err" :key="err" class="err">{{ err }}</div></Transition>
      <div class="row end">
        <button type="button" class="btn ghost" @click="cancelPin">取消</button>
        <button type="submit" class="btn primary" :disabled="busy || !pin">解锁</button>
      </div>
    </form>
  </Modal>
</template>

<style scoped>
.pin { font-size: 24px; letter-spacing: 10px; text-align: center; margin: 12px 0 4px; }
.err { min-height: 1.5em; }
.end { justify-content: flex-end; margin-top: 8px; }
</style>
