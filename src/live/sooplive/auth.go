package sooplive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"

	"github.com/bililive-go/bililive-go/src/pkg/utils"
)

const (
	authCheckURL = "https://afevent2.sooplive.com/api/get_private_info.php"
	loginURL     = "https://login.sooplive.com/app/LoginAction.php"
	loginOKCode  = 1
)

var (
	authCheckEndpoint = authCheckURL
	loginEndpoint     = loginURL
	newHTTPClient     = utils.CreateDefaultClient
	verifyCookieFunc  = VerifyCookieString
	loginAndGetCookie = LoginAndGetCookie
	nowFunc           = time.Now

	verifyCookieCacheTTL = 2 * time.Minute
	verifyCookieCacheMax = 128
	verifyCookieCache    = map[string]cachedVerifyResult{}
	verifyCookieCacheMu  sync.Mutex
	verifyCookieGroup    singleflight.Group
	loginGroup           singleflight.Group

	playSoopURL, _    = url.Parse("https://play.sooplive.com/")
	loginSoopURL, _   = url.Parse("https://login.sooplive.com/")
	liveSoopURL, _    = url.Parse("https://live.sooplive.com/")
	afeventSoopURL, _ = url.Parse("https://afevent2.sooplive.com/")
)

type cachedVerifyResult struct {
	Result    CookieVerifyResult
	ExpiresAt time.Time
}

// CookieVerifyResult 表示 Soop 登录态校验结果。
// 目前平台接口最稳定的判断方式是检查 CHANNEL.LOGIN_ID 是否存在且非空。
type CookieVerifyResult struct {
	IsLogin bool   `json:"isLogin"`
	LoginID string `json:"login_id,omitempty"`
}

// LoginResult 表示通过账号密码登录后返回给上层的结果。
// 这里同时携带拼好的 Cookie 字符串和二次校验结果，方便后端直接落盘并反馈前端。
type LoginResult struct {
	Cookie   string             `json:"cookie"`
	Verify   CookieVerifyResult `json:"verify"`
	Username string             `json:"username"`
}

// VerifyCookieString 使用 Soop 的私有信息接口校验 Cookie 是否仍然有效。
// 该函数用于：
// 1. 前端“验证 Cookie”按钮；
// 2. 录制前的登录态预检；
// 3. 登录成功后的二次确认。
func VerifyCookieString(cookie string) (*CookieVerifyResult, error) {
	client := newHTTPClient()
	req, err := http.NewRequest(http.MethodGet, authCheckEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Soop 登录态校验请求失败: %w", err)
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", defaultOrigin)
	req.Header.Set("Referer", defaultOrigin+"/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Soop 登录态校验接口失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("soop 登录态校验接口返回异常状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 Soop 登录态校验响应失败: %w", err)
	}

	loginID := gjson.GetBytes(body, "CHANNEL.LOGIN_ID").String()
	return &CookieVerifyResult{
		IsLogin: loginID != "",
		LoginID: loginID,
	}, nil
}

// VerifyCookieStringCached 使用带缓存的方式校验 Cookie。
// 适用于轮询型状态查询和面板上的重复验证动作，避免短时间内重复请求 Soop 校验接口。
func VerifyCookieStringCached(cookie string) (*CookieVerifyResult, error) {
	return verifyCookieWithCache(cookie)
}

// LoginAndGetCookie 使用账号密码调用 Soop 登录接口换取 Cookie。
// 该函数不会落盘配置，仅负责完成一次登录动作并返回可持久化的 Cookie 字符串。
// 如果返回错误，既可能是认证失败，也可能是平台接口异常或登录后校验未通过。
func LoginAndGetCookie(username, password string) (*LoginResult, error) {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("soop 登录失败：账号或密码为空")
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Soop 登录 CookieJar 失败: %w", err)
	}

	client := newHTTPClient()
	client.Jar = jar

	form := url.Values{
		"szWork":        {"login"},
		"szType":        {"json"},
		"szUid":         {username},
		"szPassword":    {password},
		"isSaveId":      {"true"},
		"isSavePw":      {"false"},
		"isSaveJoin":    {"false"},
		"isLoginRetain": {"Y"},
	}

	req, err := http.NewRequest(http.MethodPost, loginEndpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("创建 Soop 登录请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", defaultOrigin)
	req.Header.Set("Referer", defaultOrigin+"/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Soop 登录接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 Soop 登录响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("soop 登录接口返回异常状态码: %d", resp.StatusCode)
	}

	resultCode := int(gjson.GetBytes(body, "RESULT").Int())
	if resultCode != loginOKCode {
		return nil, fmt.Errorf("soop 登录失败，平台返回业务码: %d", resultCode)
	}

	cookie := buildCookieStringFromJar(jar)
	if cookie == "" {
		return nil, fmt.Errorf("soop 登录成功，但未从响应中提取到有效 Cookie")
	}

	verify, err := VerifyCookieString(cookie)
	if err != nil {
		return nil, fmt.Errorf("soop 登录成功，但登录态二次校验失败: %w", err)
	}
	if verify == nil || !verify.IsLogin {
		return nil, fmt.Errorf("soop 登录成功，但登录态二次校验未通过")
	}

	return &LoginResult{
		Cookie:   cookie,
		Verify:   *verify,
		Username: username,
	}, nil
}

// buildCookieStringFromJar 从多个 Soop 相关域名合并 Cookie。
// Soop 的登录链路涉及多个子域，因此不能只读取单个 host 下的 Cookie。
func buildCookieStringFromJar(jar http.CookieJar) string {
	if jar == nil {
		return ""
	}

	merged := make(map[string]string)
	order := make([]string, 0)
	for _, targetURL := range []*url.URL{playSoopURL, loginSoopURL, liveSoopURL, afeventSoopURL} {
		for _, cookie := range jar.Cookies(targetURL) {
			if cookie == nil || cookie.Name == "" {
				continue
			}
			if _, exists := merged[cookie.Name]; !exists {
				order = append(order, cookie.Name)
			}
			merged[cookie.Name] = cookie.Value
		}
	}

	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, fmt.Sprintf("%s=%s", name, merged[name]))
	}
	return strings.Join(parts, "; ")
}

func verifyCookieWithCache(cookie string) (*CookieVerifyResult, error) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return &CookieVerifyResult{}, nil
	}

	if cached, ok := loadCachedVerifyResult(cookie); ok {
		return cached, nil
	}

	value, err, _ := verifyCookieGroup.Do(cookie, func() (any, error) {
		if cached, ok := loadCachedVerifyResult(cookie); ok {
			return cached, nil
		}

		result, err := verifyCookieFunc(cookie)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return &CookieVerifyResult{}, nil
		}

		storeCachedVerifyResult(cookie, result)
		return cloneCookieVerifyResult(result), nil
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	return cloneCookieVerifyResult(value.(*CookieVerifyResult)), nil
}

func loadCachedVerifyResult(cookie string) (*CookieVerifyResult, bool) {
	verifyCookieCacheMu.Lock()
	defer verifyCookieCacheMu.Unlock()

	cacheKey := buildVerifyCookieCacheKey(cookie)
	entry, ok := verifyCookieCache[cacheKey]
	if !ok {
		return nil, false
	}
	if !nowFunc().Before(entry.ExpiresAt) {
		delete(verifyCookieCache, cacheKey)
		return nil, false
	}
	return cloneCookieVerifyResult(&entry.Result), true
}

func storeCachedVerifyResult(cookie string, result *CookieVerifyResult) {
	if result == nil {
		return
	}

	verifyCookieCacheMu.Lock()
	defer verifyCookieCacheMu.Unlock()

	cleanupExpiredVerifyCookieCacheLocked()
	if len(verifyCookieCache) >= verifyCookieCacheMax {
		evictOldestVerifyCookieCacheLocked()
	}

	verifyCookieCache[buildVerifyCookieCacheKey(cookie)] = cachedVerifyResult{
		Result:    *cloneCookieVerifyResult(result),
		ExpiresAt: nowFunc().Add(verifyCookieCacheTTL),
	}
}

func loginAndGetCookieWithSingleflight(username, password string) (*LoginResult, error) {
	key := username + "\x00" + password
	value, err, _ := loginGroup.Do(key, func() (any, error) {
		result, err := loginAndGetCookie(username, password)
		if err != nil {
			return nil, err
		}
		return cloneLoginResult(result), nil
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	return cloneLoginResult(value.(*LoginResult)), nil
}

// autoLoginCooldown 是同一账号两次自动登录之间的最小间隔。
// 平台可能持续判未登录而登录接口仍回成功，此时 30 秒一轮的轮询会变成不间断的密码撞库，
// 最容易触发风控锁号；冷却只推迟下一次请求，不改变"私有房间必须带 Cookie"的语义。
// 成功与否都要记：只挡失败的话，"登录成功但仍判未登录"这条风暴正好绕开。
// 面板上的手动登录走 LoginAndGetCookie，不受这里限制。
const autoLoginCooldown = 10 * time.Minute

var (
	autoLoginMu       sync.Mutex
	autoLoginAttempts = map[string]time.Time{}
	autoLoginGroup    singleflight.Group
)

// remainingAutoLoginCooldown 返回该账号还需等待多久才允许再次自动登录，0 表示允许。
// 按账号而非账号+密码计，因为要挡的是同一账号被自动反复登录，无论期间凭证串怎么变。
func remainingAutoLoginCooldown(username string) time.Duration {
	autoLoginMu.Lock()
	defer autoLoginMu.Unlock()
	last, ok := autoLoginAttempts[username]
	if !ok {
		return 0
	}
	if remaining := autoLoginCooldown - nowFunc().Sub(last); remaining > 0 {
		return remaining
	}
	delete(autoLoginAttempts, username)
	return 0
}

// markAutoLoginAttempt 在发起自动登录前记录时刻。
func markAutoLoginAttempt(username string) {
	autoLoginMu.Lock()
	defer autoLoginMu.Unlock()
	autoLoginAttempts[username] = nowFunc()
}

// errAutoLoginCoolingDown 表示"这一次根本没发登录请求，因为同账号刚试过"。
// 调用方要据此区分"被冷却挡住"与"登录确实失败"：前者应该先去找别人已经换好的 Cookie，
// 而不是让这一路房间带着失效凭证一直等到下一个登录窗口。
var errAutoLoginCoolingDown = errors.New("soop 自动登录处于冷却期")

// autoLoginWithCooldown 把"查冷却、记时刻、发起登录"合成一次不可分割的动作。
// 同账号的多个房间共用一个 singleflight 键，因此同时到达时仍只发一次登录并共享结果；
// 只有先后到达的后续轮询才会被冷却挡住。
func autoLoginWithCooldown(username, password string) (*LoginResult, error) {
	value, err, _ := autoLoginGroup.Do(username, func() (any, error) {
		if remaining := remainingAutoLoginCooldown(username); remaining > 0 {
			return nil, fmt.Errorf("%w，%.0f 秒后重试", errAutoLoginCoolingDown, remaining.Seconds())
		}
		markAutoLoginAttempt(username)
		return loginAndGetCookieWithSingleflight(username, password)
	})
	if err != nil {
		return nil, err
	}
	return cloneLoginResult(value.(*LoginResult)), nil
}

func cloneCookieVerifyResult(result *CookieVerifyResult) *CookieVerifyResult {
	if result == nil {
		return nil
	}
	cloned := *result
	return &cloned
}

func cloneLoginResult(result *LoginResult) *LoginResult {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.Verify = *cloneCookieVerifyResult(&result.Verify)
	return &cloned
}

func buildVerifyCookieCacheKey(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}

func cleanupExpiredVerifyCookieCacheLocked() {
	now := nowFunc()
	for key, entry := range verifyCookieCache {
		if !now.Before(entry.ExpiresAt) {
			delete(verifyCookieCache, key)
		}
	}
}

func evictOldestVerifyCookieCacheLocked() {
	var (
		oldestKey string
		oldestAt  time.Time
		found     bool
	)
	for key, entry := range verifyCookieCache {
		if !found || entry.ExpiresAt.Before(oldestAt) {
			oldestKey = key
			oldestAt = entry.ExpiresAt
			found = true
		}
	}
	if found {
		delete(verifyCookieCache, oldestKey)
	}
}
