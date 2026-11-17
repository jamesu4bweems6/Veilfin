package server

import (
	"sync"
	"time"

	"veilfin/internal/proto"
)

const (
	sessionMaxAge  = 24 * time.Hour
	sessionIdleTTL = 2 * time.Hour
	maxSessions    = 10000
)

type serverSession struct {
	*proto.Session

	mu            sync.Mutex
	userID        int64
	unlockedUntil time.Time
	loginFails    int
	lastSeen      time.Time
	rids          map[string]time.Time // 防重放：最近见过的请求 ID
}

// setUser 切换会话身份。身份变化时一律清除解锁状态，防止继承上一个用户的 PIN 解锁。
func (ss *serverSession) setUser(uid int64) {
	ss.mu.Lock()
	ss.userID, ss.unlockedUntil, ss.loginFails = uid, time.Time{}, 0
	ss.mu.Unlock()
}

func (ss *serverSession) uid() int64 {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.userID
}

func (ss *serverSession) unlocked(now time.Time) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return now.Before(ss.unlockedUntil)
}

type sessionStore struct {
	mu sync.Mutex
	m  map[string]*serverSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: map[string]*serverSession{}}
}

func (st *sessionStore) add(s *proto.Session, now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.m) >= maxSessions { // 防止握手洪水耗尽内存：优先淘汰未登录的、最久未活动的会话
		var oldestK string
		var oldest time.Time
		oldestAuthed := true
		for k, v := range st.m {
			v.mu.Lock()
			ls, authed := v.lastSeen, v.userID != 0
			v.mu.Unlock()
			better := oldestK == "" || (oldestAuthed && !authed) || (authed == oldestAuthed && ls.Before(oldest))
			if better {
				oldestK, oldest, oldestAuthed = k, ls, authed
			}
		}
		delete(st.m, oldestK)
	}
	st.m[string(s.SID)] = &serverSession{Session: s, lastSeen: now, rids: map[string]time.Time{}}
}

func (st *sessionStore) get(sid []byte, now time.Time) *serverSession {
	st.mu.Lock()
	ss := st.m[string(sid)]
	st.mu.Unlock()
	if ss == nil {
		return nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if now.Sub(ss.Created) > sessionMaxAge || now.Sub(ss.lastSeen) > sessionIdleTTL {
		return nil
	}
	return ss
}

// touch 在消息解密成功后更新活跃时间。
func (ss *serverSession) touch(now time.Time) {
	ss.mu.Lock()
	ss.lastSeen = now
	ss.mu.Unlock()
}

func (st *sessionStore) gc(now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, ss := range st.m {
		ss.mu.Lock()
		if now.Sub(ss.Created) > sessionMaxAge || now.Sub(ss.lastSeen) > sessionIdleTTL {
			delete(st.m, k)
		} else {
			for rid, t := range ss.rids {
				if now.Sub(t) > ridTTL {
					delete(ss.rids, rid)
				}
			}
		}
		ss.mu.Unlock()
	}
}

func (st *sessionStore) clear() {
	st.mu.Lock()
	st.m = map[string]*serverSession{}
	st.mu.Unlock()
}

// logoutUser 让某个用户的所有会话退出登录（禁用账号、删除账号时使用）。
func (st *sessionStore) logoutUser(uid int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, ss := range st.m {
		ss.mu.Lock()
		if ss.userID == uid {
			ss.userID = 0
			ss.unlockedUntil = time.Time{}
		}
		ss.mu.Unlock()
	}
}
