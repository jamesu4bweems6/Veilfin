import { createRouter, createWebHashHistory } from 'vue-router'
import { ref } from 'vue'
import { state } from './store.js'

// depth 表示页面层级：同级之间切换用交叉淡化，进入更深层用“推入”，返回用“弹回”
const routes = [
  { path: '/', component: () => import('./views/Home.vue'), meta: { depth: 0 } },
  { path: '/login', component: () => import('./views/Login.vue'), meta: { public: true, depth: 0 } },
  { path: '/setup', component: () => import('./views/Setup.vue'), meta: { public: true, depth: 0 } },
  { path: '/library/:id', component: () => import('./views/Library.vue'), props: true, meta: { depth: 0 } },
  { path: '/item/:id', component: () => import('./views/Item.vue'), props: true, meta: { depth: 1 } },
  { path: '/play/:id', component: () => import('./views/Player.vue'), props: true, meta: { fullscreen: true, depth: 2 } },
  { path: '/search', component: () => import('./views/Search.vue'), meta: { depth: 0 } },
  { path: '/history', component: () => import('./views/History.vue'), meta: { depth: 0 } },
  { path: '/settings', component: () => import('./views/Settings.vue'), meta: { depth: 0 } },
  {
    // 管理后台共用一个布局：标签栏常驻，切换时只有内容区过渡，指示器可以平滑滑动
    path: '/admin',
    component: () => import('./views/admin/AdminLayout.vue'),
    meta: { admin: true, depth: 0, section: 'admin' },
    children: [
      { path: '', component: () => import('./views/admin/Dashboard.vue') },
      { path: 'users', component: () => import('./views/admin/Users.vue') },
      { path: 'libraries', component: () => import('./views/admin/Libraries.vue') },
      { path: 'server', component: () => import('./views/admin/Server.vue') },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

// 当前页面切换动画的名字（App.vue 里的 <Transition> 使用）
export const transitionName = ref('page-tab')

// 页面切换时，等旧页面离场后再滚动到顶部 / 恢复位置，避免动画过程中页面跳动
let scrollResolver = null
export function onPageLeft() {
  scrollResolver?.()
  scrollResolver = null
}

const router = createRouter({
  history: createWebHashHistory(),
  routes,
  scrollBehavior(to, from, saved) {
    if (to.path === from.path) return false // 只改了查询参数（筛选条件）：保持位置
    const target = saved || { top: 0 }
    return new Promise((resolve) => {
      scrollResolver = () => resolve(target)
      setTimeout(() => {
        if (scrollResolver) onPageLeft()
      }, 260)
    })
  },
})

router.beforeEach((to) => {
  if (state.needsSetup && to.path !== '/setup') return '/setup'
  if (!state.needsSetup && to.path === '/setup') return state.me ? '/' : '/login'
  if (!to.meta.public && !state.me) return { path: '/login', query: to.fullPath !== '/' ? { next: to.fullPath } : {} }
  if (to.meta.admin && !state.me?.isAdmin) return '/'
  if (to.path === '/login' && state.me) return '/'
})

router.afterEach((to, from) => {
  if (!from.matched.length) {
    transitionName.value = 'page-tab'
    return
  }
  const a = from.meta.depth ?? 0
  const b = to.meta.depth ?? 0
  if (to.meta.fullscreen) transitionName.value = 'page-zoom'
  else if (b > a) transitionName.value = 'page-push'
  else if (b < a) transitionName.value = 'page-pop'
  else transitionName.value = 'page-tab'
})

export default router
