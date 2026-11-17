// IndexedDB 封装（网页模式用来把对象密钥交给 Service Worker）。
// 注意：public/sw.js 中有一份相同结构的实现，修改时两边保持一致。
const DB_NAME = 'veilfin' // sw.js 里有同样的值
const VERSION = 1

let dbp = null

export function openDB() {
  if (!dbp) {
    dbp = new Promise((resolve, reject) => {
      const req = indexedDB.open(DB_NAME, VERSION)
      req.onupgradeneeded = () => {
        const db = req.result
        if (!db.objectStoreNames.contains('blobs')) db.createObjectStore('blobs')
        if (!db.objectStoreNames.contains('meta')) db.createObjectStore('meta')
      }
      req.onsuccess = () => resolve(req.result)
      req.onerror = () => reject(req.error)
    })
  }
  return dbp
}

function done(tx) {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
    tx.onabort = () => reject(tx.error)
  })
}

export async function putMany(store, entries) {
  const db = await openDB()
  const tx = db.transaction(store, 'readwrite')
  const s = tx.objectStore(store)
  for (const [k, v] of entries) s.put(v, k)
  return done(tx)
}

export async function put(store, key, value) {
  return putMany(store, [[key, value]])
}

export async function get(store, key) {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const req = db.transaction(store).objectStore(store).get(key)
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

export async function clear(store) {
  const db = await openDB()
  const tx = db.transaction(store, 'readwrite')
  tx.objectStore(store).clear()
  return done(tx)
}
