// Package douyu 中的 cookie 自动续期实现（refresh.go）
//
// 背景：斗鱼主站登录 cookie（acf_* 全家桶）仅约 6 天有效（Set-Cookie Max-Age≈529200s），
// 且网页端不做被动 Set-Cookie 轮换，长时间运行的录制会因 cookie 过期而退回匿名流（约 5 分钟被 CDN 掐断）。
// 扫码登录确认成功那一次，passport 域会通过 /japi/scan/auth 轮询响应下发长期凭证 LTP0（数月有效）。
// 续期协议（实测抓包）：
//  1. GET passport.douyu.com/lapi/passport/iframe/safeAuth?...（Cookie: LTP0=..）
//     -> 302 Location 指向 www.douyu.com/api/passport/login?...&loginType=safeAuth 的一次性登录 URL
//  2. 带 LTP0 跟一跳该 URL -> 下发全套新的 acf_* 登录 cookie（safeAuth 不会重发 LTP0）
//  3. 用 www.douyu.com/wgapi/livenc/liveweb/follow/top3 探针校验 HTTP 200 且业务 error==0 => 登录态有效
//     （关注主播此刻可能都没开播，room_list 为空同样算有效）
package douyu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// ErrLoginInvalid 表示"凭证本身失效"（LTP0 过期、换票缺字段、探针判定未登录），
// 需要用户重新扫码；区别于网络抖动等临时性错误（不应据此打扰用户）。
var ErrLoginInvalid = errors.New("斗鱼登录凭证已失效")

// safeAuthUrl/probeUrl 声明为 var 而非 const：两者都是"续期是否真的成功"的唯一判定点，
// 需要在单元测试里指向 httptest 服务器，才能覆盖风控页/非 JSON 响应等真实故障形态。
var (
	safeAuthUrl = "https://passport.douyu.com/lapi/passport/iframe/safeAuth"
	probeUrl    = "https://www.douyu.com/wgapi/livenc/liveweb/follow/top3"
)

const (
	// cookieRenewInterval 主站 acf_* 实际约 6 天有效（Set-Cookie Max-Age≈529200s），
	// 取 3 天续期一次，留足一倍缓冲，兼顾"不频繁轮换"与"宕机后仍能赶在过期前补续"。
	cookieRenewInterval = 3 * 24 * time.Hour
	// renewRetryBackoff 换票失败（LTP0 疑似已失效）时的重试退避，避免每小时疯狂打接口。
	renewRetryBackoff = 6 * time.Hour
	// renewInvalidRetryBackoff 已经判定票据失效（NeedRescan 已置位）之后的重试退避。
	// 6 小时那一档是为"可能只是一次风控误判"留的自愈窗口；但用户不会在几小时内重扫，
	// 若一直用同一张已判死的票据每 6 小时打一次 safeAuth，就是每天 4 次反复敲登录类接口
	// （正是最容易触发风控的那一类），同时每天 4 次重写 config.yml。
	renewInvalidRetryBackoff = 24 * time.Hour
	// renewEscalateAfter 网络类失败的静默上限：主站 cookie Max-Age≈6.1 天，
	// 距上次成功超过这个天数还续不上，就不再是"稍后再试"，登录态必然已过期。
	renewEscalateAfter = 6 * 24 * time.Hour
	// mainSiteCookieMaxAge 是主站 acf_* 登录 cookie 的有效期（实测 Set-Cookie Max-Age=529200s≈6.1 天）。
	mainSiteCookieMaxAge = 529200 * time.Second
	// deadCookieMargin 判定"这份 cookie 确实已经过期"时额外留的余量。
	// 必须留：有效期是 6.1 天，若按 6 天整数就认定过期，会在最后几个小时里
	// 把一份**还能用**的登录态主动降级成匿名（丢清晰度、丢权益）。
	deadCookieMargin = 24 * time.Hour
)

// ShouldEscalateTempFailure 判断一次"临时性失败"是否应升级为需重新扫码的提醒。
// lastSuccessAt==0 表示该字段引入后还从未自动续期成功过，此时不升级（避免对历史配置
// 凭空报错）；一旦某次续期成功写入该时间戳，判定即生效。
func ShouldEscalateTempFailure(lastSuccessAt int64, now time.Time) bool {
	if lastSuccessAt == 0 {
		return false
	}
	return now.Unix()-lastSuccessAt > int64(renewEscalateAfter/time.Second)
}

// ShouldRenewNow 判断本次是否该真正发起换票。续期日程（NextRefreshAt）是相对当前时钟写下的绝对时间戳，
// 一旦系统时钟被回拨或纠正（时区修正、NTP 校正、长期睡眠的虚拟机），"还没到点"就成了假信号：
// 按日程还要等好几天，而主站 cookie 的 6 天有效期是按真实流逝的时间在耗，结果静默退回匿名录制。
// 因此除了"到点"，再允许两个提前理由：日程基准落在未来（时钟被改过）、距上次成功已超过整个有效期。
// 后者复用 renewEscalateAfter，避免与"临时失败升级"各存一个阈值而漂移。
// lastSuccessAt==0 时不参与提前判断：那只是引入该字段前的历史配置，否则会绕过 6 小时退避反复打接口。
//
// 两个提前理由都必须让位给"最近刚写下过的退避日程"：退避时间是 now+6h（临时失败）或 now+24h
// （已判定票据失效），落在最长那一档之内说明这是上一次失败后特意安排的下次重试点。
// 若此时仍按"时钟异常/距上次成功过久"提前换票，safeAuth 这种登录类接口就会被每小时的检查循环
// 反复调用（正是退避想避免的），且有触发风控的风险。
// 成功后的日程是 now+3 天，远在窗口之外，所以时钟被回拨时的兜底逻辑不受影响。
func ShouldRenewNow(nextRefreshAt, lastSuccessAt int64, now time.Time) bool {
	if nextRefreshAt == 0 {
		return true
	}
	if now.Unix() >= nextRefreshAt {
		return true
	}
	if lastSuccessAt == 0 {
		return false
	}
	if nextRefreshAt-now.Unix() <= int64(renewInvalidRetryBackoff/time.Second) {
		return false
	}
	return now.Unix() < lastSuccessAt || now.Unix()-lastSuccessAt > int64(renewEscalateAfter/time.Second)
}

// NextRenewAt 返回续期成功后"下次应续期"的 Unix 秒时间戳。
func NextRenewAt(now time.Time) int64 { return now.Add(cookieRenewInterval).Unix() }

// RenewBackoffAt 返回换票失败后"下次重试"的 Unix 秒时间戳（较短退避）。
func RenewBackoffAt(now time.Time) int64 { return now.Add(renewRetryBackoff).Unix() }

// RenewInvalidRetryAt 返回"已判定失效（NeedRescan 已置位）之后下次重试"的 Unix 秒时间戳（较长退避）。
func RenewInvalidRetryAt(now time.Time) int64 { return now.Add(renewInvalidRetryBackoff).Unix() }

// LoginCookieKnownExpired 判断"这份主站登录 cookie 是否已有确凿证据表明彻底失效"：
// 必须同时满足「存有长期凭证（说明这份登录态本来该被自动续期）」与
// 「距上次拿到新鲜登录态已超过主站 cookie 的整个有效期 + 余量」。
//
// 为什么需要它：斗鱼的 cookie 与签名请求里我们一律"有值就发"，而一份确凿过期的 acf_*
// 再发出去不可能换到任何登录权益，只可能带来更坏的结果（服务端把"带非法登录态的请求"
// 整条拒掉，表现为房间完全录不上，而不是退回匿名流）。所以到了这个判定就主动停用，
// 让行为回到"从未登录过"那条已被验证过的路径上。
//
// 之所以不看 NeedRescan：进程停摆十天后重启，盘上标记仍是"健康"，但那份 cookie 显然已过期，
// 此时后台还在 30 秒后才第一次尝试换票，录制侧不该继续拿它当有效登录态用。
// 反过来，没有 LTP0 就没有"本该续期却续不上"的证据，那份手填 cookie 无从判断，照旧发出。
func LoginCookieKnownExpired(ltp0 string, lastSuccessAt int64, now time.Time) bool {
	if ltp0 == "" || lastSuccessAt == 0 {
		return false
	}
	return now.Unix()-lastSuccessAt > int64((mainSiteCookieMaxAge+deadCookieMargin)/time.Second)
}

// RenewEscalateAfter 返回"临时失败该升级为失效提醒"的时间阈值。
func RenewEscalateAfter() time.Duration { return renewEscalateAfter }

// refreshRequiredKeys 换票后判定"登录态成功"的必需字段集（实测抓包：缺任一字段即视为未真正登录）
var refreshRequiredKeys = []string{"acf_uid", "acf_auth", "acf_stk", "acf_ltkid", "acf_username", "acf_biz", "acf_ct"}

// RefreshLoginCookie 用 passport 域长期凭证 LTP0 通过 safeAuth 换取全新的主站登录 cookie。
// 返回字段级 map（仅含本次换到的 cookie 字段）；调用方负责与已有 Cookies 合并写回。
// 若必需字段缺失或探针判定登录态无效则返回 error（通常意味着 LTP0 也已过期，需重新扫码）。
func RefreshLoginCookie(ctx context.Context, ltp0, dyDid string) (map[string]string, error) {
	if strings.TrimSpace(ltp0) == "" {
		return nil, fmt.Errorf("缺少 LTP0，无法续期")
	}
	cookie := "LTP0=" + ltp0
	if dyDid != "" {
		cookie = "dy_did=" + dyDid + "; " + cookie
	}
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	start := fmt.Sprintf("%s?client_id=1&t=%s&_=%s&callback=cb", safeAuthUrl, ts, ts)

	fields, status, err := followExchangeChain(ctx, start, cookie)
	if err != nil {
		return nil, err
	}
	// 只有终点是 200 的响应才可能携带 acf_*；4xx/5xx（风控、限流、接口变更）与"LTP0 过期"无关，
	// 必须按临时性错误返回，否则会因为"字段缺失"误判凭证失效而推送假的重扫提醒。
	// status==0 表示跳数用尽仍未落到终态响应，同样按临时错误处理。
	if status != http.StatusOK {
		return nil, fmt.Errorf("safeAuth 换票链未正常结束（终点 HTTP %d），暂不判定登录态是否失效", status)
	}
	var missing []string
	for _, k := range refreshRequiredKeys {
		if fields[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w：换票响应缺少必需字段 %v，LTP0 可能已过期", ErrLoginInvalid, missing)
	}
	if err := verifyLoginCookie(ctx, cookieString(fields)); err != nil {
		return nil, err
	}
	// 设备号原样带回，"要不要落盘"由写回侧决定（见 ApplyDeviceIDPolicy）：
	// safeAuth 换票时我们不带站点设备号，斗鱼这次可能顺手签发一个全新随机 dy_did/acf_devid，
	// 它既可能是唯一可用的设备号来源，也可能是每 3 天轮换一次的诱因，只有写回侧看得见现有 cookie。
	return fields, nil
}

// followExchangeChain 从 safeAuth 起始 URL 手动逐跳跟随 302（每跳都带 LTP0 cookie），
// 累积中间跳与终点跳下发的 Set-Cookie。斗鱼在 302 Location 的下一跳才真正下发 acf_*。
// 额外返回终点状态码（0 表示跳数用尽仍未落到终点），供调用方区分"接口异常"与"凭证失效"。
func followExchangeChain(ctx context.Context, startURL, cookie string) (map[string]string, int, error) {
	fields := make(map[string]string)
	cur := startURL
	finalStatus := 0
	for hop := 0; hop < 8; hop++ {
		req, err := http.NewRequestWithContext(ctx, "GET", cur, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", douyuWebUAString)
		req.Header.Set("Referer", "https://www.douyu.com/")
		req.Header.Set("Cookie", cookie)
		resp, err := douyuHTTPClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		for _, c := range resp.Cookies() {
			// 空值 Set-Cookie 是"清除该 cookie"的写法：若后一跳把前一跳刚拿到的字段清成空串，
			// 会让必需字段校验误判为"LTP0 已过期"、或把配置里已有的好值覆盖掉。
			if c.Value == "" {
				continue
			}
			fields[c.Name] = c.Value
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if loc == "" {
				finalStatus = resp.StatusCode
				break
			}
			next, err := resolveDouyuRedirect(cur, loc)
			if err != nil {
				return nil, 0, err
			}
			cur = next
			continue
		}
		finalStatus = resp.StatusCode
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		break
	}
	return fields, finalStatus, nil
}

// resolveDouyuRedirect 解析（可能为 //host 或相对形式的）Location，并做斗鱼域名白名单校验，防 SSRF。
func resolveDouyuRedirect(base, loc string) (string, error) {
	if strings.HasPrefix(loc, "//") {
		loc = "https:" + loc
	}
	bu, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	lu, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	ref := bu.ResolveReference(lu)
	if err := checkDouyuURL(ref.String()); err != nil {
		return "", err
	}
	return ref.String(), nil
}

// verifyLoginCookie 用换到的 cookie 打关注房间探针，确认登录态真实有效。
// 判定标准：HTTP 200 且响应 JSON 里确实带 error 字段、其值为 0 才算已登录
// （关注主播此刻可能都没开播，room_list 为空同样算有效）。
// 探针返回明确的"未登录"业务码 -> 归为 ErrLoginInvalid；HTTP/网络异常/响应不是预期 JSON
// -> 视为临时错误。特别注意：风控页会以 HTTP 200 + HTML 形态返回，此时 error 字段不存在，
// 若按"缺省即 0"判定就会把无效 cookie 当成功落盘并清掉失效标记，导致数日静默退回匿名录制。
func verifyLoginCookie(ctx context.Context, cookie string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", probeUrl, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", douyuWebUAString)
	req.Header.Set("Referer", "https://www.douyu.com/")
	req.Header.Set("Cookie", cookie)
	resp, err := douyuHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("续期后探针 HTTP %d（临时性错误）", resp.StatusCode)
	}
	errField := gjson.GetBytes(body, "error")
	if !errField.Exists() {
		return fmt.Errorf("续期后探针响应不含 error 字段（疑似风控页或接口变更），暂不判定登录态有效, body=%s", truncateForLog(body))
	}
	if e := errField.Int(); e != 0 {
		return fmt.Errorf("%w：探针 error=%d, msg=%q", ErrLoginInvalid, e, gjson.GetBytes(body, "msg").String())
	}
	return nil
}

// cookieString 把字段 map 拼成 "k1=v1; k2=v2" 形式的 cookie 串（按 key 排序，保证输出稳定）
func cookieString(fields map[string]string) string {
	return strings.Join(sortedCookiePairs(fields), "; ")
}

// sortedCookiePairs 按字段名有序地输出 "k=v" 列表。
// map 遍历顺序随机，不排序会让每次续期都把 config.yml 里的 cookie 整行重排，产生无意义的配置 diff。
func sortedCookiePairs(fields map[string]string) []string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+fields[k])
	}
	return parts
}
