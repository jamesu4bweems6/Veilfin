package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"veilfin/internal/api"
	"veilfin/internal/auth"
	"veilfin/internal/db"
	"veilfin/internal/mediacrypt"
)

func init() {
	register("sys.info", public, rpcSysInfo)
	register("sys.ping", public, func(s *Server, c *call) (any, error) {
		return map[string]int64{"time": s.now().UnixMilli()}, nil
	})
	register("setup.create", public, rpcSetup)
	register("auth.login", public, rpcLogin)
	register("auth.resume", public, rpcResume)
	register("auth.logout", public, rpcLogout)
	register("auth.me", user, func(s *Server, c *call) (any, error) { return s.me(c), nil })
	register("auth.blobToken", user, rpcBlobToken)
	register("auth.changePassword", user, rpcChangePassword)
	register("auth.setPin", user, rpcSetPin)
	register("auth.unlock", user, rpcUnlock)
	register("auth.lock", user, func(s *Server, c *call) (any, error) {
		c.ss.mu.Lock()
		c.ss.unlockedUntil = time.Time{}
		c.ss.mu.Unlock()
		// 让已签发的（可能带解锁标志的）访问令牌立即失效
		if err := s.DB.BumpTokenGen(c.ctx, c.u.ID); err != nil {
			return nil, err
		}
		s.invalidateAccess()
		return s.me(c), nil
	})
	register("auth.setPrefs", user, func(s *Server, c *call) (any, error) {
		var p struct {
			BlurRestricted bool `json:"blurRestricted"`
		}
		if err := c.bind(&p); err != nil {
			return nil, err
		}
		if err := s.DB.SetBlur(c.ctx, c.u.ID, p.BlurRestricted); err != nil {
			return nil, err
		}
		c.u.BlurRestricted = p.BlurRestricted
		return s.me(c), nil
	})
}

type meResult struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	IsAdmin        bool   `json:"isAdmin"`
	HasPin         bool   `json:"hasPin"`
	Unlocked       bool   `json:"unlocked"`
	UnlockedUntil  int64  `json:"unlockedUntil"`
	BlurRestricted bool   `json:"blurRestricted"`
	ServerName     string `json:"serverName"`
}

func (s *Server) me(c *call) *meResult {
	c.ss.mu.Lock()
	until := c.ss.unlockedUntil
	c.ss.mu.Unlock()
	unlocked := c.u.PinHash == "" || s.now().Before(until)
	r := &meResult{ID: c.u.ID, Username: c.u.Username, IsAdmin: c.u.IsAdmin, HasPin: c.u.PinHash != "",
		Unlocked: unlocked, BlurRestricted: c.u.BlurRestricted, ServerName: s.Lib.Settings(c.ctx).ServerName}
	if c.u.PinHash != "" && unlocked {
		r.UnlockedUntil = until.Unix()
	}
	return r
}

func rpcSysInfo(s *Server, c *call) (any, error) {
	return map[string]any{
		"needsSetup": s.SetupCode() != "",
		"serverName": s.Lib.Settings(c.ctx).ServerName,
		"version":    s.cfg.Version,
		"loggedIn":   c.u != nil,
	}, nil
}

func validUsername(name string) error {
	name = strings.TrimSpace(name)
	if len([]rune(name)) < 2 || len([]rune(name)) > 32 {
		return errf(api.CodeBadRequest, "用户名长度应为 2 到 32 个字符")
	}
	if strings.ContainsAny(name, "\x00\r\n\t") {
		return errf(api.CodeBadRequest, "用户名包含非法字符")
	}
	return nil
}

func rpcSetup(s *Server, c *call) (any, error) {
	s.setupMu.Lock() // 防止并发初始化创建出多个管理员
	defer s.setupMu.Unlock()
	if s.setupCode == "" {
		return nil, errf(api.CodeForbidden, "已经初始化过了")
	}
	var p struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToUpper(strings.TrimSpace(p.Code))), []byte(s.setupCode)) != 1 {
		s.slowFail()
		return nil, errf(api.CodeUnauthorized, "初始化代码不正确（见服务端启动日志）")
	}
	if err := validUsername(p.Username); err != nil {
		return nil, err
	}
	if err := auth.ValidatePassword(p.Password); err != nil {
		return nil, errf(api.CodeBadRequest, "%v", err)
	}
	uid, err := s.DB.CreateUser(c.ctx, strings.TrimSpace(p.Username), auth.Hash(p.Password), true)
	if err != nil {
		return nil, err
	}
	s.setupCode = ""
	if s.cfg.InitialMedia != "" {
		if libID, err := s.DB.CreateLibrary(c.ctx, &db.Library{Name: "视频", Kind: "home", Scraper: "none", Paths: []string{s.cfg.InitialMedia}}); err == nil {
			s.Lib.EnqueueScan(libID, false)
			s.Lib.RestartWatcher()
		}
	}
	return s.finishLogin(c, uid)
}

func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

// finishLogin 把会话绑定到用户并签发“记住登录”令牌。
func (s *Server) finishLogin(c *call, uid int64) (any, error) {
	u, err := s.DB.UserByID(c.ctx, uid)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	if err := s.DB.AddLoginToken(c.ctx, hashToken(tok), uid, s.now().Add(loginTokenTTL).Unix()); err != nil {
		return nil, err
	}
	c.ss.setUser(uid)
	c.u = u
	return map[string]any{"token": tok, "me": s.me(c)}, nil
}

func rpcLogin(s *Server, c *call) (any, error) {
	var p struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	c.ss.mu.Lock()
	fails := c.ss.loginFails
	c.ss.mu.Unlock()
	if fails >= maxLoginFails {
		return nil, errf(api.CodeTooMany, "失败次数过多，请稍后重试")
	}
	name := strings.TrimSpace(p.Username)
	u, err := s.DB.UserByName(c.ctx, name)
	if errors.Is(err, db.ErrNotFound) {
		auth.VerifyDummy(p.Password) // 用户名不存在时也耗费同样的时间
		if s.ghostLocked(name) {
			return nil, errf(api.CodeTooMany, "账号因多次密码错误被临时锁定，请稍后再试")
		}
		s.ghostFail(name)
		return nil, s.loginFailed(c, nil)
	}
	if err != nil {
		return nil, err
	}
	if u.LockedUntil > s.now().Unix() {
		auth.VerifyDummy(p.Password)
		return nil, errf(api.CodeTooMany, "账号因多次密码错误被临时锁定，请稍后再试")
	}
	if auth.Verify(u.PwHash, p.Password) != nil {
		return nil, s.loginFailed(c, u)
	}
	if u.Disabled {
		return nil, errf(api.CodeForbidden, "账号已被禁用")
	}
	s.DB.RecordLoginSuccess(c.ctx, u.ID)
	return s.finishLogin(c, u.ID)
}

func (s *Server) loginFailed(c *call, u *db.User) error {
	c.ss.mu.Lock()
	c.ss.loginFails++
	c.ss.mu.Unlock()
	if u != nil {
		s.DB.RecordLoginFailure(c.ctx, u.ID, maxLoginFails, s.now().Add(lockoutTime).Unix())
	}
	s.slowFail()
	return errf(api.CodeUnauthorized, "用户名或密码错误")
}

func rpcResume(s *Server, c *call) (any, error) {
	var p api.TokenParams
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if p.Token == "" {
		return nil, errf(api.CodeUnauthorized, "登录已失效")
	}
	uid, err := s.DB.UseLoginToken(c.ctx, hashToken(p.Token), s.now().Add(loginTokenTTL).Unix())
	if err != nil {
		return nil, errf(api.CodeUnauthorized, "登录已失效")
	}
	u, err := s.DB.UserByID(c.ctx, uid)
	if err != nil || u.Disabled {
		return nil, errf(api.CodeUnauthorized, "登录已失效")
	}
	c.ss.setUser(uid)
	c.u = u
	return s.me(c), nil
}

func rpcLogout(s *Server, c *call) (any, error) {
	var p api.TokenParams
	c.bind(&p)
	if p.Token != "" {
		s.DB.DeleteLoginToken(c.ctx, hashToken(p.Token))
	}
	c.ss.setUser(0)
	return struct{}{}, nil
}

func rpcBlobToken(s *Server, c *call) (any, error) {
	now := s.now()
	exp := now.Add(blobTokenTTL)
	var flags byte
	c.ss.mu.Lock()
	until := c.ss.unlockedUntil
	c.ss.mu.Unlock()
	if c.u.PinHash == "" {
		flags |= mediacrypt.TokenUnlocked
	} else if now.Before(until) {
		flags |= mediacrypt.TokenUnlocked
		if until.Before(exp) {
			exp = until // 解锁到期后，带解锁标志的令牌也随之失效
		}
	}
	return api.BlobToken{Token: s.keys.UserToken(c.u.ID, c.u.TokenGen, flags, exp), Exp: exp.Unix()}, nil
}

func rpcChangePassword(s *Server, c *call) (any, error) {
	var p struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if auth.Verify(c.u.PwHash, p.Old) != nil {
		s.slowFail()
		return nil, errf(api.CodeUnauthorized, "原密码不正确")
	}
	if err := auth.ValidatePassword(p.New); err != nil {
		return nil, errf(api.CodeBadRequest, "%v", err)
	}
	if err := s.DB.SetPassword(c.ctx, c.u.ID, auth.Hash(p.New)); err != nil {
		return nil, err
	}
	// 改密码会吊销所有记住登录的令牌，给当前会话重新签发一个
	return s.finishLogin(c, c.u.ID)
}

func rpcSetPin(s *Server, c *call) (any, error) {
	var p struct {
		Password string `json:"password"`
		Pin      string `json:"pin"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if auth.Verify(c.u.PwHash, p.Password) != nil {
		s.slowFail()
		return nil, errf(api.CodeUnauthorized, "密码不正确")
	}
	hash := ""
	if p.Pin != "" {
		if err := auth.ValidatePin(p.Pin); err != nil {
			return nil, errf(api.CodeBadRequest, "%v", err)
		}
		hash = auth.Hash(p.Pin)
	}
	if err := s.DB.SetPin(c.ctx, c.u.ID, hash); err != nil {
		return nil, err
	}
	c.u.PinHash = hash
	s.invalidateAccess()
	c.ss.mu.Lock()
	c.ss.unlockedUntil = time.Time{}
	c.ss.mu.Unlock()
	return s.me(c), nil
}

func rpcUnlock(s *Server, c *call) (any, error) {
	var p struct {
		Pin string `json:"pin"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if c.u.PinHash == "" {
		return s.me(c), nil
	}
	now := s.now()
	if c.u.PinLockedUntil > now.Unix() {
		return nil, errf(api.CodeTooMany, "PIN 错误次数过多，已锁定 %d 分钟", (c.u.PinLockedUntil-now.Unix())/60+1)
	}
	if auth.Verify(c.u.PinHash, p.Pin) != nil {
		// 失败次数记在数据库里：重新握手、换设备都不会清零
		s.DB.RecordPinFailure(c.ctx, c.u.ID, maxLoginFails, now.Add(lockoutTime).Unix())
		s.slowFail()
		return nil, errf(api.CodeUnauthorized, "PIN 不正确")
	}
	s.DB.ResetPinFailures(c.ctx, c.u.ID)
	c.ss.mu.Lock()
	c.ss.unlockedUntil = now.Add(unlockDuration)
	c.ss.mu.Unlock()
	return s.me(c), nil
}

// ---- 不存在的用户名的失败计数（只在内存中） ----

func (s *Server) ghostLocked(name string) bool {
	s.ghostMu.Lock()
	defer s.ghostMu.Unlock()
	g := s.ghosts[strings.ToLower(name)]
	return g != nil && s.now().Before(g.until)
}

func (s *Server) ghostFail(name string) {
	s.ghostMu.Lock()
	defer s.ghostMu.Unlock()
	if len(s.ghosts) > 10000 {
		s.ghosts = map[string]*ghost{}
	}
	key := strings.ToLower(name)
	g := s.ghosts[key]
	if g == nil || s.now().After(g.until.Add(lockoutTime)) {
		g = &ghost{}
		s.ghosts[key] = g
	}
	g.fails++
	if g.fails >= maxLoginFails {
		g.until = s.now().Add(lockoutTime)
	}
}
