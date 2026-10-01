// Package douyu 中的扫码登录实现（auth.go）
// 协议来源：斗鱼官方登录页（passport.douyu.com/index/login）实测抓包：
// 1. POST /scan/generateCode        -> {error:0, data:{expire:300, url:<二维码内容>, code:<32位hex>}}
// 2. GET  /japi/scan/auth?time&code -> error: -2 未扫码 / 1 已扫码待确认 / 0 确认成功(data.url) / -1 二维码失效
// 3. 确认成功后 GET data.url（JSONP），登录 cookie 由该响应 Set-Cookie 下发
package douyu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	qrGenerateUrl  = "https://passport.douyu.com/scan/generateCode"
	qrPollUrl      = "https://passport.douyu.com/japi/scan/auth"
	qrLoginReferer = "https://passport.douyu.com/index/login"

	// CookieHost 是斗鱼登录 cookie 在 configs.Cookies 中使用的 host key
	CookieHost = "www.douyu.com"
)

// douyuWebUAString 与录制侧一致的浏览器 UA，避免签名/登录接口风控
const douyuWebUAString = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// douyuHTTPClient 登录相关请求专用 client：不自动跟随重定向，
// 由 exchangeLoginCookie 手动逐跳处理以保留中间跳的 Set-Cookie。
var douyuHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var qrCodeReg = regexp.MustCompile(`^[0-9a-f]{32}$`)

// QRLoginState 扫码登录的轮询状态
type QRLoginState string

const (
	QRStateWaiting  QRLoginState = "waiting"  // 客户端还未扫码
	QRStateScanned  QRLoginState = "scanned"  // 已扫码，等待手机端确认
	QRStateSuccess  QRLoginState = "success"  // 确认成功，已换取并保存登录 cookie
	QRStateExpired  QRLoginState = "expired"  // 二维码失效/被取消
	QRStateRejected QRLoginState = "rejected" // 手机端拒绝授权
	// QRStateSaveFailed 表示"手机端已确认登录，但后端保存配置失败"（磁盘满/配置只读等）。
	// 必须与"轮询本身失败"区分：前者反复重扫也没用，要先解决配置可写。
	QRStateSaveFailed QRLoginState = "save_failed"
)

// QRLoginSession 一次扫码登录会话
type QRLoginSession struct {
	Code   string `json:"code"`   // 轮询用的会话 code
	QRUrl  string `json:"qr_url"` // 二维码内容（斗鱼中间页链接）
	Expire int    `json:"expire"` // 有效期（秒）
}

// QRPollResult 轮询结果；成功时附带登录 cookie 与用户信息。
// Cookie/LTP0/DyDid 一律 json:"-"：它们只用于后端内部传递（写配置、续期），
// 绝不能出现在任何 JSON 响应里——pollDouyuQRCode 在 writeJSON 前会显式置空作为第二道保险。
type QRPollResult struct {
	State    QRLoginState `json:"state"`
	Nickname string       `json:"nickname,omitempty"`
	UserID   string       `json:"uid,omitempty"`
	LTP0     string       `json:"-"` // passport 域长期凭证，用于后续 safeAuth 自动续期
	DyDid    string       `json:"-"` // 可选设备 id，随 LTP0 一起持久化
	Cookie   string       `json:"-"` // 仅成功时有值，由后端直接落盘
	Msg      string       `json:"msg,omitempty"`
}

// GenerateLoginQRCode 向后端代理请求斗鱼登录二维码。
// ctx 用于在 HTTP 客户端断开或超时后中止上游请求。
func GenerateLoginQRCode(ctx context.Context) (*QRLoginSession, error) {
	form := url.Values{"client_id": {"1"}, "isMultiAccount": {"0"}}
	req, err := http.NewRequestWithContext(ctx, "POST", qrGenerateUrl, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", douyuWebUAString)
	req.Header.Set("Referer", qrLoginReferer)
	req.Header.Set("Origin", "https://passport.douyu.com")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := doDouyuRequest(req)
	if err != nil {
		return nil, err
	}
	// 斗鱼业务错误一律 HTTP 200 + error 字段；字段不存在说明这不是一个 API 响应
	// （风控拦截页也会以 200 返回 HTML）。此处不带上正文，避免上游页面进前端错误与日志。
	errField := gjson.GetBytes(body, "error")
	if !errField.Exists() {
		return nil, fmt.Errorf("斗鱼二维码接口响应不是预期 JSON（疑似风控页或接口已变更），响应长度 %d 字节", len(body))
	}
	if errField.Int() != 0 {
		return nil, fmt.Errorf("斗鱼返回错误: %s", strings.TrimSpace(gjson.GetBytes(body, "data").String()))
	}
	code := gjson.GetBytes(body, "data.code").String()
	qrUrl := gjson.GetBytes(body, "data.url").String()
	if !qrCodeReg.MatchString(code) || qrUrl == "" {
		return nil, fmt.Errorf("斗鱼二维码响应异常（code/url 不完整），响应长度 %d 字节", len(body))
	}
	return &QRLoginSession{
		Code:   code,
		QRUrl:  qrUrl,
		Expire: int(gjson.GetBytes(body, "data.expire").Int()),
	}, nil
}

// PollLoginQRCode 轮询一次扫码状态；确认成功时自动完成 cookie 换取
func PollLoginQRCode(ctx context.Context, code string) (*QRPollResult, error) {
	if !qrCodeReg.MatchString(code) {
		return nil, errors.New("非法的二维码 code")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s?time=%d&code=%s", qrPollUrl, time.Now().UnixMilli(), code), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", douyuWebUAString)
	req.Header.Set("Referer", qrLoginReferer)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	body, pollCookies, err := doDouyuRequestWithCookies(req)
	if err != nil {
		return nil, err
	}
	// 同 GenerateLoginQRCode：error 字段不存在说明拿到的不是 API 响应（风控页会以 200 返回 HTML），
	// 若按"缺省即 0"继续走，就会被当成"已确认成功"去换票，最终吐出一句"斗鱼登录成功响应缺少回调 URL"。
	errField := gjson.GetBytes(body, "error")
	if !errField.Exists() {
		return nil, fmt.Errorf("斗鱼扫码状态轮询响应不是预期 JSON（疑似风控页或接口已变更），响应长度 %d 字节", len(body))
	}
	msg := gjson.GetBytes(body, "msg").String()
	switch errField.Int() {
	case 0:
		exchangeUrl := gjson.GetBytes(body, "data.url").String()
		if exchangeUrl == "" {
			return nil, fmt.Errorf("斗鱼登录确认成功但响应缺少回调 URL（接口可能已变更），响应长度 %d 字节", len(body))
		}
		cookie, infoBody, trace, err := exchangeLoginCookie(ctx, exchangeUrl)
		if err != nil {
			return nil, err
		}
		if cookie == "" {
			return nil, fmt.Errorf("斗鱼登录确认成功但未获得 Set-Cookie，接口可能已变更; %s", trace)
		}
		// 探针确认这份 cookie 真的算登录态：斗鱼改版后跳转链也可能"收敛"到一个不下发 acf_* 的页面，
		// 只看 cookie 非空就把残缺登录态写盘，还会顺手把下次续期排到 3 天后——面板显示已登录、实际静默不录。
		// 只有探针给出明确的"未登录"业务码才判失败；风控页、网络抖动这类 ambiguous 异常按原样放行，
		// 不能因为探针这一次没打通就把用户刚完成的一次真实登录作废。
		if perr := verifyLoginCookie(ctx, cookie); perr != nil && errors.Is(perr, ErrLoginInvalid) {
			return nil, fmt.Errorf("斗鱼登录 Cookie 未通过登录态校验，请重新扫码: %w; %s", perr, trace)
		}
		nickname := pickFirstString(infoBody, "nickname", "user_nickname", "uname", "data.nickname")
		userID := pickFirstString(infoBody, "uid", "user_id", "id", "data.uid")
		// JSONP 回调结构可能变化，cookie 中的 acf_uid/acf_nickname 是更稳定的来源
		if userID == "" {
			userID = cookieValue(cookie, "acf_uid")
		}
		if nickname == "" {
			if enc := cookieValue(cookie, "acf_nickname"); enc != "" {
				if dec, e := url.QueryUnescape(enc); e == nil {
					nickname = dec
				}
			}
		}
		// LTP0 由轮询 error=0 响应的 Set-Cookie 下发（passport 域），是后续续期的关键长期凭证
		ltp0 := cookieFromList(pollCookies, "LTP0")
		if ltp0 == "" {
			ltp0 = cookieValue(cookie, "LTP0")
		}
		dyDid := cookieFromList(pollCookies, "dy_did")
		if dyDid == "" {
			dyDid = cookieValue(cookie, "dy_did")
		}
		return &QRPollResult{
			State:    QRStateSuccess,
			Nickname: nickname,
			UserID:   userID,
			Cookie:   cookie,
			LTP0:     ltp0,
			DyDid:    dyDid,
		}, nil
	case 1:
		return &QRPollResult{State: QRStateScanned, Msg: msg}, nil
	case -2:
		return &QRPollResult{State: QRStateWaiting, Msg: msg}, nil
	case -1:
		return &QRPollResult{State: QRStateExpired, Msg: msg}, nil
	default:
		return &QRPollResult{State: QRStateRejected, Msg: msg}, nil
	}
}

// doDouyuRequest 执行一次斗鱼上游请求并返回响应体（限制 1MB）
func doDouyuRequest(req *http.Request) ([]byte, error) {
	body, _, err := doDouyuRequestWithCookies(req)
	return body, err
}

// doDouyuRequestWithCookies 与 doDouyuRequest 相同，但额外返回响应的 Set-Cookie，
// 供扫码成功时提取 passport 域 LTP0 等长期凭证。
// 4xx/5xx 直接判为失败：斗鱼业务错误一律 HTTP 200 + error 字段，非 2xx 是风控页/网关页，
// 其 HTML 正文若原样返回，会被上层当成"JSON 里 error 字段缺省"继续解析，
// 最终拼出"斗鱼登录成功响应缺少回调 URL, body: <!DOCTYPE html>…"这种既误导又外泄上游原文的错误。
func doDouyuRequestWithCookies(req *http.Request) ([]byte, []*http.Cookie, error) {
	resp, err := douyuHTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("斗鱼登录接口返回 HTTP %d（可能被风控或接口已变更），请求 %s", resp.StatusCode, req.URL.Path)
	}
	return body, resp.Cookies(), nil
}

// cookieFromList 从 http.Cookie 列表中取指定字段的值
func cookieFromList(cookies []*http.Cookie, name string) string {
	for _, c := range cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// exchangeLoginCookie 请求登录成功回调 URL（JSONP），从 Set-Cookie 收集登录态。
// 注意：斗鱼可能在 302 跳转链的中间跳下发登录 cookie，
// 而 http.Client 自动跟随重定向时只能读到最后一跳的 Set-Cookie，
// 因此这里手动逐跳请求并累积所有 Set-Cookie（每跳都校验 douyu 域名白名单）。
func exchangeLoginCookie(ctx context.Context, rawUrl string) (cookie string, jsonpBody string, trace string, err error) {
	if err = checkDouyuURL(rawUrl); err != nil {
		return "", "", "", err
	}
	seen := make(map[string]string) // 字段名 -> 值，后一跳覆盖前一跳（终点才是权威登录态）
	var hops []string               // 跳转链诊断信息（仅记录 cookie 名，不记录值）
	cur := rawUrl
	var finalBody string
	finalStatus := 0
	converged := false // 是否走到了非跳转的终点响应；false 表示 8 跳预算用尽
	for hop := 0; hop < 8; hop++ {
		req, err := http.NewRequestWithContext(ctx, "GET", cur, nil)
		if err != nil {
			return "", "", "", err
		}
		req.Header.Set("User-Agent", douyuWebUAString)
		req.Header.Set("Referer", qrLoginReferer)
		resp, err := douyuHTTPClient.Do(req)
		if err != nil {
			return "", "", "", err
		}
		names := make([]string, 0, len(resp.Cookies()))
		for _, c := range resp.Cookies() {
			names = append(names, c.Name)
			// 空值 Set-Cookie 是"清除该 cookie"的写法，不值得写入登录态；缺失会让下游字段保持更早的有效值
			if c.Value == "" {
				continue
			}
			seen[c.Name] = c.Value
		}
		// 一次性登录 token 就在 query 里，这条诊断串会随错误冒泡到前端与日志，只保留 scheme://host/path
		hopURL := cur
		if u, e := url.Parse(cur); e == nil {
			hopURL = u.Scheme + "://" + u.Host + u.Path
		}
		hops = append(hops, fmt.Sprintf("%s -> %d (Set-Cookie: %v)", hopURL, resp.StatusCode, names))
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc, _ := resp.Location()
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if loc == nil {
				// 3xx 却不给 Location：跳转链在此断裂，后面再也拿不到任何 Set-Cookie，
				// 把已经攒到的半截 cookie 当登录成功落盘，表现为面板显示已登录而实际录不上。
				trace = strings.Join(hops, "; ")
				return "", "", trace, fmt.Errorf("斗鱼登录回调返回 HTTP %d 但未给出跳转地址，登录链中断; %s", resp.StatusCode, trace)
			}
			if err = checkDouyuURL(loc.String()); err != nil {
				return "", "", strings.Join(hops, "; "), err
			}
			cur = loc.String()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return "", "", strings.Join(hops, "; "), err
		}
		finalBody = string(body)
		finalStatus = resp.StatusCode
		converged = true
		break
	}
	trace = strings.Join(hops, "; ")
	if !converged {
		// 8 跳仍未落到终点响应：斗鱼加长或循环了跳转链。此前这里带着"半截 cookie + nil error"直接返回，
		// 调用方只看 cookie 非空就判定登录成功，残缺登录态会连同 3 天后的续期排期一起落盘。
		return "", "", trace, fmt.Errorf("斗鱼登录回调连续 8 跳仍未到达终点，接口可能已变更; %s", trace)
	}
	if finalStatus >= 400 {
		return "", "", trace, fmt.Errorf("斗鱼登录回调终点返回 HTTP %d（风控或接口变更），未获得登录态; %s", finalStatus, trace)
	}
	return strings.Join(sortedCookiePairs(seen), "; "), stripJSONP(finalBody), trace, nil
}

// checkDouyuURL 只允许斗鱼官方域名，防止被诱导请求任意地址（SSRF）
func checkDouyuURL(rawUrl string) error {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return fmt.Errorf("回调 URL 解析失败: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("拒绝非 HTTPS 回调: %s", u.Scheme)
	}
	if u.Host != "douyu.com" && !strings.HasSuffix(u.Host, ".douyu.com") {
		return fmt.Errorf("拒绝请求非斗鱼域名回调: %s", u.Host)
	}
	return nil
}

// stripJSONP 从 JSONP 响应中提取 JSON 主体（appClient_json_callback({...});）
func stripJSONP(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return ""
	}
	return raw[start : end+1]
}

func pickFirstString(jsonBody string, paths ...string) string {
	for _, p := range paths {
		if v := gjson.Get(jsonBody, p).String(); v != "" {
			return v
		}
	}
	return ""
}

// cookieValue 从 "k1=v1; k2=v2" 形式的 cookie 串中取出指定字段的原始值
func cookieValue(cookieStr, name string) string {
	for _, item := range strings.Split(cookieStr, ";") {
		item = strings.TrimSpace(item)
		if v, ok := strings.CutPrefix(item, name+"="); ok {
			return v
		}
	}
	return ""
}

func truncateForLog(body []byte) string {
	const max = 200
	if len(body) > max {
		return string(body[:max]) + "..."
	}
	return string(body)
}
