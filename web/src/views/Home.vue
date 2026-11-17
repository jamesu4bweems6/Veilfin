<script setup>
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { state, rpc, toastError, requestPin } from '../store.js'
import { kindNames, placeholderStyle } from '../util.js'
import MediaRow from '../components/MediaRow.vue'
import MediaCard from '../components/MediaCard.vue'
import Icon from '../components/Icon.vue'

const router = useRouter()
const data = ref(null)
const loading = ref(true)

async function load() {
  try {
    data.value = await rpc('home.get')
    state.me = data.value.me
  } catch (e) {
    toastError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

const shapeOf = (kind) => (kind === 'home' ? 'thumb' : 'poster')

async function openLib(l) {
  if (l.locked) {
    if (!(await requestPin())) return
  }
  router.push(`/library/${l.id}`)
}
</script>

<template>
  <div class="page home">
    <Transition name="fade" mode="out-in">
      <div v-if="loading" key="skel" class="skeleton-home">
        <div v-for="r in 3" :key="r" class="skel-row">
          <div class="skel skel-title" />
          <div class="skel-cards">
            <div v-for="c in 6" :key="c" class="skel skel-card" :style="{ animationDelay: c * 80 + 'ms' }" />
          </div>
        </div>
      </div>
      <div v-else-if="data" key="data">
        <div v-if="!data.libraries.length" class="empty rise">
          <p>还没有可访问的媒体库。</p>
          <router-link v-if="state.me?.isAdmin" to="/admin/libraries" class="btn primary">添加媒体库</router-link>
          <p v-else class="muted">请联系管理员为你开通媒体库权限。</p>
        </div>

        <MediaRow v-if="data.libraries.length" title="我的媒体" shape="tile" :index="0">
          <button v-for="(l, i) in data.libraries" :key="l.id" class="tile rise" :style="{ ...placeholderStyle(l.name), '--i': i }" @click="openLib(l)">
            <div class="tile-name">{{ l.name }}</div>
            <div class="tile-sub">{{ kindNames[l.kind] }} · {{ l.count }} 个</div>
            <Icon v-if="l.restricted" :name="l.locked ? 'lock' : 'unlock'" class="tile-lock" />
          </button>
        </MediaRow>

        <MediaRow v-if="data.continue.length" title="继续观看" shape="thumb" :items="data.continue" :index="1" />

        <MediaRow v-for="(row, ri) in data.latest" :key="row.library.id" :title="`最近添加 · ${row.library.name}`" :to="`/library/${row.library.id}`"
          :shape="shapeOf(row.library.kind)" :index="ri + 2">
          <MediaCard v-for="(it, i) in row.items" :key="it.id" :item="it" :shape="shapeOf(row.library.kind)" :index="i" />
        </MediaRow>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.tile { position: relative; aspect-ratio: 16 / 9; border-radius: 12px; padding: 14px; text-align: left; display: flex; flex-direction: column; justify-content: flex-end;
  overflow: hidden; isolation: isolate; }
.tile:hover { box-shadow: 0 0 0 3px var(--accent), 0 16px 32px rgba(0,0,0,.45); transform: translateY(-3px); }
.tile-name { font-size: 18px; font-weight: 600; }
.tile-sub { font-size: 12px; color: rgba(255,255,255,.7); }
.tile-lock { position: absolute; top: 12px; right: 12px; opacity: .8; transition: transform .4s var(--spring-bouncy); }
.tile:hover .tile-lock { transform: scale(1.15) rotate(-8deg); }
.empty .btn { margin-top: 14px; }
.skel-row { margin: 28px 0 8px; }
.skel-title { width: 180px; height: 22px; margin-bottom: 14px; }
.skel-cards { display: grid; grid-auto-flow: column; grid-auto-columns: clamp(220px, 24vw, 340px); gap: 16px; overflow: hidden; }
.skel-card { aspect-ratio: 16 / 9; border-radius: 8px; }
</style>
