package douyu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 续期链的每一跳都要过域名白名单，这里把可接受/可拒绝的形态固定下来（防 SSRF）
func TestCheckDouyuURL(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"https://www.douyu.com/api/passport/login?a=1", false},
		{"https://douyu.com/", false},
		{"https://passport.douyu.com/lapi/passport/iframe/safeAuth", false},
		{"http://www.douyu.com/", true},            // 明文一律拒绝，LTP0 不允许裸传输
		{"https://douyu.com.evil.com/x", true},     // 后缀伪装
		{"https://evil.com/#douyu.com", true},      // 片段里藏域名
		{"https://douyu.com:8443/@evil.com", true}, // 带端口的 host 不匹配白名单，保守拒绝
		{"ftp://www.douyu.com/", true},
		{"://bad", true},
	}
	for _, c := range cases {
		if err := checkDouyuURL(c.raw); (err != nil) != c.wantErr {
			t.Errorf("checkDouyuURL(%q) err=%v 期望报错=%v", c.raw, err, c.wantErr)
		}
	}
}

func TestResolveDouyuRedirect(t *testing.T) {
	const base = "https://passport.douyu.com/lapi/passport/iframe/safeAuth"
	// 斗鱼 safeAuth 的 Location 实测是协议相对形式 //www.douyu.com/...
	got, err := resolveDouyuRedirect(base, "//www.douyu.com/api/passport/login?uid=1")
	if err != nil || got != "https://www.douyu.com/api/passport/login?uid=1" {
		t.Fatalf("协议相对 Location 解析错误: %q err=%v", got, err)
	}
	got, err = resolveDouyuRedirect(base, "/lapi/passport/other")
	if err != nil || got != "https://passport.douyu.com/lapi/passport/other" {
		t.Fatalf("相对 Location 解析错误: %q err=%v", got, err)
	}
	if _, err := resolveDouyuRedirect(base, "https://evil.com/cb"); err == nil {
		t.Error("跨域 Location 未被拒绝")
	}
	if _, err := resolveDouyuRedirect(base, "javascript:alert(1)"); err == nil {
		t.Error("非 http(s) Location 未被拒绝")
	}
}

func TestCookieValueAndFromList(t *testing.T) {
	ck := "acf_uid=42; LTP0=long.ticket-value; empty="
	if v := cookieValue(ck, "acf_uid"); v != "42" {
		t.Errorf("cookieValue=%q", v)
	}
	if v := cookieValue(ck, "missing"); v != "" {
		t.Errorf("不存在的字段应为空串, got=%q", v)
	}
	list := []*http.Cookie{{Name: "LTP0", Value: "abc"}, {Name: "dy_did", Value: "did"}}
	if v := cookieFromList(list, "dy_did"); v != "did" {
		t.Errorf("cookieFromList=%q", v)
	}
	if v := cookieFromList(list, "nope"); v != "" {
		t.Errorf("cookieFromList 缺失字段应为空串, got=%q", v)
	}
}

// 缺 LTP0 属于"根本无法续期"，必须是普通错误而非"凭证失效"，避免误报"登录已失效"
func TestRefreshLoginCookieWithoutLTP0(t *testing.T) {
	_, err := RefreshLoginCookie(context.Background(), "  ", "")
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if errors.Is(err, ErrLoginInvalid) {
		t.Error("缺少 LTP0 不应归类为凭证失效（此时应静默跳过，不提醒用户）")
	}
}

// 扫码结果的序列化面：cookie 与长期凭证绝不能进 JSON（前端只需 state/uid/nickname）。
// handler 里另有"下发前置空"的第二道保险，这里锁住结构体本身。
func TestQRPollResultHidesCredentials(t *testing.T) {
	b, err := json.Marshal(QRPollResult{
		State:    QRStateSuccess,
		Nickname: "nick",
		UserID:   "42",
		Cookie:   "acf_auth=secret",
		LTP0:     "LTP0=secret-ticket",
		DyDid:    "dy_did=secret-device",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, forbidden := range []string{"secret", "ltp0", "cookie", "dy_did"} {
		if strings.Contains(strings.ToLower(got), forbidden) {
			t.Errorf("响应体泄露了 %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, `"state":"success"`) || !strings.Contains(got, `"uid":"42"`) {
		t.Errorf("状态字段丢失: %s", got)
	}
}

func TestStripJSONP(t *testing.T) {
	if got := stripJSONP("cb({\"error\":0,\"data\":{\"url\":\"x\"}});"); got != `{"error":0,"data":{"url":"x"}}` {
		t.Errorf("stripJSONP=%q", got)
	}
	if got := stripJSONP("no json here"); got != "" {
		t.Errorf("无 JSON 时应返回空串, got=%q", got)
	}
}

// withProbeURL 把探针地址临时指向测试服务器（probeUrl 声明为 var 正是为此）
func withProbeURL(t *testing.T, url string) {
	t.Helper()
	old := probeUrl
	probeUrl = url
	t.Cleanup(func() { probeUrl = old })
}

// 探针是"换到的 cookie 是否真的可用"的唯一依据，必须失败关闭：
// 斗鱼风控会以 HTTP 200 + HTML 形态返回，若把缺失的 error 字段当成 0 就误报成功，
// 后台随即清掉失效标记并把无效 cookie 再用三天——全程无通知，录制静默退回匿名、约 5 分钟断一次。
func TestVerifyLoginCookieIsFailClosed(t *testing.T) {
	var status int32 = http.StatusOK
	var body atomic.Value
	body.Store(`{"error":0}`)
	var gotCookie atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie.Store(r.Header.Get("Cookie"))
		w.WriteHeader(int(atomic.LoadInt32(&status)))
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	t.Cleanup(srv.Close)
	withProbeURL(t, srv.URL)

	cases := []struct {
		name        string
		httpStatus  int32
		respBody    string
		wantErr     bool
		wantInvalid bool // 是否应归类为"凭证失效"（需扫码），否则视为临时错误
	}{
		{name: "已登录", respBody: `{"error":0,"data":{"room_list":[]}}`},
		{name: "业务码未登录", respBody: `{"error":-3,"msg":"未登录"}`, wantErr: true, wantInvalid: true},
		{name: "200加HTML风控页", respBody: "<!DOCTYPE html><html>verify</html>", wantErr: true},
		{name: "200加JSON无error字段", respBody: `{"code":0}`, wantErr: true},
		{name: "200加空响应体", respBody: "", wantErr: true},
		{name: "服务端500", httpStatus: 500, respBody: `{"error":0}`, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := c.httpStatus
			if st == 0 {
				st = http.StatusOK // 用例里省略即表示 200
			}
			atomic.StoreInt32(&status, st)
			body.Store(c.respBody)
			err := verifyLoginCookie(context.Background(), "acf_uid=1; acf_auth=x")
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v 期望报错=%v", err, c.wantErr)
			}
			if err != nil && c.wantInvalid != errors.Is(err, ErrLoginInvalid) {
				t.Fatalf("错误归类不对: err=%v 是否凭证失效=%v 期望=%v", err, errors.Is(err, ErrLoginInvalid), c.wantInvalid)
			}
			if err == nil && gotCookie.Load().(string) == "" {
				t.Error("探针请求未携带待验证的 cookie")
			}
		})
	}
}

// 后一跳用空值清除前一跳刚拿到的字段时不得覆盖有效值，
// 否则必需字段校验会把仍然有效的 LTP0 误判为已过期，让用户白扫一次码。
func TestFollowExchangeChainIgnoresEmptyCookies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "acf_auth", Value: "good.value"})
		http.SetCookie(w, &http.Cookie{Name: "acf_stk", Value: ""}) // 清除写法
		http.SetCookie(w, &http.Cookie{Name: "acf_uid", Value: "42"})
	}))
	t.Cleanup(srv.Close)

	fields, _, err := followExchangeChain(context.Background(), srv.URL, "LTP0=x")
	if err != nil {
		t.Fatal(err)
	}
	if fields["acf_auth"] != "good.value" || fields["acf_uid"] != "42" {
		t.Fatalf("有效字段丢失: %+v", fields)
	}
	if _, ok := fields["acf_stk"]; ok {
		t.Errorf("空值 Set-Cookie 不应写入字段表: %+v", fields)
	}
}

// 换票链终点不是 200（风控 403、网关 500）时响应里必然没有 acf_*。
// 若按"缺字段"判凭证失效，用户会收到一条"登录已失效请重新扫码"的假告警，
// 而实际只是斗鱼接口瞬时异常——正确处置是按临时错误静默退避。
func TestRefreshLoginCookieClassifiesChainStatus(t *testing.T) {
	cases := []struct {
		name        string
		finalStatus int
		wantInvalid bool
	}{
		{name: "终点500属临时错误", finalStatus: http.StatusInternalServerError},
		{name: "终点403属临时错误", finalStatus: http.StatusForbidden},
		{name: "终点200缺字段属失效", finalStatus: http.StatusOK, wantInvalid: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "follow/top3") {
					_, _ = io.WriteString(w, `{"error":0,"data":{"room_list":[]}}`)
					return
				}
				w.WriteHeader(c.finalStatus)
			}))
			t.Cleanup(srv.Close)
			withRefreshURLs(t, srv.URL)

			_, err := RefreshLoginCookie(context.Background(), "some.ticket", "")
			if err == nil {
				t.Fatal("换票链异常时不应报告成功")
			}
			if errors.Is(err, ErrLoginInvalid) != c.wantInvalid {
				t.Fatalf("错误归类不对: err=%v 判为凭证失效=%v 期望=%v", err, errors.Is(err, ErrLoginInvalid), c.wantInvalid)
			}
		})
	}
}

// 302 但没带 Location 时链条就此中断：拿到的状态码必须是这一个 3xx，
// 让调用方按临时错误处理，而不是把"没有终态响应"当成 200 后判成凭证失效。
func TestFollowExchangeChainReportsNonTerminalStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound) // 故意不设 Location
	}))
	t.Cleanup(srv.Close)
	_, status, err := followExchangeChain(context.Background(), srv.URL, "LTP0=x")
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusFound {
		t.Fatalf("应报告中断处的 302, got %d", status)
	}
}

// cookie 串顺序稳定是 config.yml 不产生无意义 diff 的前提（map 遍历本身是随机的）
func TestCookieStringIsStable(t *testing.T) {
	fields := map[string]string{"acf_uid": "1", "acf_auth": "x", "acf_stk": "y"}
	first := cookieString(fields)
	for i := 0; i < 50; i++ {
		if got := cookieString(fields); got != first {
			t.Fatalf("第 %d 次拼接不一致:\n%s\n%s", i, first, got)
		}
	}
	if !strings.HasPrefix(first, "acf_auth=") {
		t.Errorf("应按字段名升序拼接, got=%s", first)
	}
}

// withRefreshURLs 把换票起始地址与探针地址一并指向测试服务器（两者声明为 var 正是为此）
func withRefreshURLs(t *testing.T, base string) {
	t.Helper()
	oldSafe, oldProbe := safeAuthUrl, probeUrl
	safeAuthUrl = base + "/lapi/passport/iframe/safeAuth"
	probeUrl = base + "/wgapi/livenc/liveweb/follow/top3"
	t.Cleanup(func() {
		safeAuthUrl, probeUrl = oldSafe, oldProbe
	})
}

// 换票时我们不带站点设备号，斗鱼会顺手签发一个全新的随机 dy_did/acf_devid。
// 续期侧只负责原样带回（探针也要用这套字段），"丢还是落盘"由写回侧单点决定：
// 现有 cookie 已有设备号则挡掉这次的新值，一个都没有则补种，见 ApplyDeviceIDPolicy。
// 若在此处就剥离，写回侧将无法区分"上游没发设备号"与"上游发了但要挡"，缺失场景补种不了。
func TestRefreshLoginCookieReturnsDeviceIDForWriteBackPolicy(t *testing.T) {
	var probeCookie atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "follow/top3") {
			probeCookie.Store(r.Header.Get("Cookie"))
			_, _ = io.WriteString(w, `{"error":0,"data":{"room_list":[]}}`)
			return
		}
		for _, kv := range [][2]string{
			{"acf_uid", "42"}, {"acf_auth", "auth"}, {"acf_stk", "stk"}, {"acf_ltkid", "ltk"},
			{"acf_username", "uname"}, {"acf_biz", "biz"}, {"acf_ct", "ct"},
			{"dy_did", "fresh-random-did"}, {"acf_devid", "fresh-random-devid"},
		} {
			http.SetCookie(w, &http.Cookie{Name: kv[0], Value: kv[1]})
		}
	}))
	t.Cleanup(srv.Close)
	withRefreshURLs(t, srv.URL)

	fields, err := RefreshLoginCookie(context.Background(), "valid.ticket", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range refreshRequiredKeys {
		if fields[k] == "" {
			t.Errorf("必需字段 %s 丢失", k)
		}
	}
	// 换票侧只负责"原样带回"，设备号要不要落盘由写回侧单点决定（见 ApplyDeviceIDPolicy）：
	// 现有 cookie 已有设备号则挡掉这次的新值，一个都没有则补种。
	for _, kv := range [][2]string{{"dy_did", "fresh-random-did"}, {"acf_devid", "fresh-random-devid"}} {
		if fields[kv[0]] != kv[1] {
			t.Errorf("设备号字段 %s 应原样带回, got=%q", kv[0], fields[kv[0]])
		}
	}
	if pc, _ := probeCookie.Load().(string); !strings.Contains(pc, "dy_did=fresh-random-did") {
		t.Errorf("探针应在写回侧取舍之前完成校验, got=%q", pc)
	}
}

// 网络类失败默认静默退避，但连续失败到 cookie 必然过期时必须升级为"需重新扫码"，
// 否则用户侧表现为长期匿名录制（约 5 分钟断一次）却任何提示都没有。
func TestShouldEscalateTempFailure(t *testing.T) {
	now := time.Unix(1893456000, 0)
	day := int64(24 * time.Hour / time.Second)
	cases := []struct {
		name        string
		lastSuccess int64
		want        bool
	}{
		{"字段引入前从未成功过：不据此报错", 0, false},
		{"3 天前刚续期成功：仍按临时错误退避", now.Add(-3 * 24 * time.Hour).Unix(), false},
		{"刚到 6 天边界：尚未确定过期", now.Add(-6*24*time.Hour + time.Hour).Unix(), false},
		{"超过 cookie 整个有效期：升级为失效提醒", now.Add(-7 * 24 * time.Hour).Unix(), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldEscalateTempFailure(c.lastSuccess, now); got != c.want {
				t.Fatalf("升级=%v 期望=%v（距上次成功 %d 天）", got, c.want, (now.Unix()-c.lastSuccess)/day)
			}
		})
	}
}

// 续期日程是相对时钟写下的绝对时间戳：时钟回拨/纠正后"未到点"就成了假信号，
// 而 cookie 有效期按真实时间流逝，静默过期就是最坏的失效形态。
func TestShouldRenewNow(t *testing.T) {
	now := time.Unix(1893456000, 0)
	day := 24 * time.Hour
	cases := []struct {
		name       string
		next, last int64
		want       bool
	}{
		{"历史配置无日程：立即续一次以自举", 0, 0, true},
		{"正常到点", now.Unix(), now.Add(-3 * day).Unix(), true},
		{"日程已过", now.Add(-time.Hour).Unix(), now.Add(-3 * day).Unix(), true},
		{"未到点且基准可信：尊重日程", now.Add(2 * day).Unix(), now.Add(-1 * day).Unix(), false},
		{"刚失败退避 6 小时：不被时钟理由绕过", now.Add(5 * time.Hour).Unix(), now.Add(-time.Hour).Unix(), false},
		{"时钟回拨到上次成功之前：日程不可信", now.Add(2 * day).Unix(), now.Add(time.Hour).Unix(), true},
		{"时钟前进后回正：距上次成功超有效期", now.Add(2 * day).Unix(), now.Add(-7 * day).Unix(), true},
		{"无成功时间戳的历史配置：只看到点", now.Add(2 * day).Unix(), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldRenewNow(c.next, c.last, now); got != c.want {
				t.Fatalf("续期=%v 期望=%v (next=%d last=%d now=%d)", got, c.want, c.next, c.last, now.Unix())
			}
		})
	}
}
