package servers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/instance"
	dy "github.com/bililive-go/bililive-go/src/live/douyu"
)

// 手工编辑"设置明文"里的斗鱼 cookie 等于换成另一份登录态，此时盘上的 LTP0 属于原账号。
// 若不清掉，后台下次排期会拿原账号换票并把它的 acf_* 合并回来——用户手改的 cookie 过几天
// 自己变了回去，且完全看不出原因。
func TestPutRawConfigClearsDouyuAuthOnDouyuCookieEdit(t *testing.T) {
	orig := configs.GetCurrentConfig()
	t.Cleanup(func() { configs.SetCurrentConfig(orig) })

	ctx := context.WithValue(context.Background(), instance.Key, &instance.Instance{
		Ctx: context.Background(),
	})

	// stored 是配置里已有的登录 cookie，submitted 是"设置明文"页保存回来的那一行
	putRaw := func(stored, submitted string) {
		t.Helper()
		base := configs.NewConfig()
		base.File = filepath.Join(t.TempDir(), "config.yml")
		base.Cookies = map[string]string{dy.CookieHost: stored}
		base.DouyuAuth = configs.DouyuAuth{LTP0: "ticket.value", NextRefreshAt: 1893456000, LastRenewSuccessAt: 1893000000}
		configs.SetCurrentConfig(base)

		yamlBody := "rpc:\n  enable: true\n  bind: \":8080\"\ninterval: 20\nout_put_path: ./\n" +
			"cookies:\n  www.douyu.com: \"" + submitted + "\"\n" +
			"douyu_auth:\n  ltp0: \"" + douyuSecretMask + "\"\n  next_refresh_at: 1893456000\n"
		body, err := json.Marshal(map[string]any{"config": yamlBody})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/api/raw-config", strings.NewReader(string(body)))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		putRawConfig(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("保存明文失败: code=%d body=%s", rec.Code, rec.Body.String())
		}
	}

	const storedCookie = "acf_uid=1; acf_auth=old.value"
	putRaw(storedCookie, storedCookie)
	// 原文未改动地保存一次：掩码要沿用真票据，否则"打开明文页点一次保存"就废掉自动续期
	if got := configs.GetCurrentConfig().DouyuAuth.LTP0; got != "ticket.value" {
		t.Fatalf("未改 cookie 时不应清除长期凭证, got %q", got)
	}

	putRaw(storedCookie, "acf_uid=2; acf_auth=another.value")
	cfg := configs.GetCurrentConfig()
	if cfg.DouyuAuth.LTP0 != "" {
		t.Errorf("手改斗鱼 cookie 后应清除长期凭证, got %q", cfg.DouyuAuth.LTP0)
	}
	if cfg.DouyuAuth.NextRefreshAt != 0 {
		t.Errorf("续期日程应一并清除, got %d", cfg.DouyuAuth.NextRefreshAt)
	}
}

// 状态接口是"续期失败"前端提示的唯一数据源：必须如实反映标记，且绝不泄露任何凭证原文。
func TestGetDouyuAuthStatusNeverLeaksSecrets(t *testing.T) {
	orig := configs.GetCurrentConfig()
	t.Cleanup(func() { configs.SetCurrentConfig(orig) })
	configs.SetCurrentConfig(&configs.Config{
		Cookies: map[string]string{dy.CookieHost: "acf_uid=42; acf_auth=secret-auth-value"},
		DouyuAuth: configs.DouyuAuth{
			LTP0:          "secret-ltp0-ticket",
			DyDid:         "secret-dy-did",
			NextRefreshAt: 1893456000,
			NeedRescan:    true,
		},
	})

	rec := httptest.NewRecorder()
	getDouyuAuthStatus(rec, httptest.NewRequest(http.MethodGet, "/api/douyu/auth/status", nil))

	body := rec.Body.String()
	for _, secret := range []string{"secret-ltp0-ticket", "secret-dy-did", "secret-auth-value", "acf_uid", "acf_auth"} {
		if strings.Contains(body, secret) {
			t.Fatalf("响应泄露了敏感内容 %q: %s", secret, body)
		}
	}

	var resp struct {
		ErrNo int                        `json:"err_no"`
		Data  map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v body=%s", err, body)
	}
	if resp.ErrNo != 0 {
		t.Fatalf("err_no=%d 期望 0", resp.ErrNo)
	}
	// 只允许这四个可见状态字段，多一个字段就是多一个凭证外泄面
	for _, k := range []string{"logged_in", "has_ltp0", "need_rescan", "next_refresh_at"} {
		if _, ok := resp.Data[k]; !ok {
			t.Errorf("缺少状态字段 %s, body=%s", k, body)
		}
	}
	if len(resp.Data) != 4 {
		t.Fatalf("字段数=%d 期望=4, got=%s", len(resp.Data), body)
	}
	if string(resp.Data["need_rescan"]) != "true" || string(resp.Data["has_ltp0"]) != "true" {
		t.Errorf("状态未如实反映配置: %s", body)
	}
}

func TestParseCookieFields(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		wantN int
	}{
		{"正常串", "acf_uid=123; acf_auth=abc", 2},
		{"值中含等号", "acf_auth=a=b=c", 1},
		{"无等号的垃圾片段被忽略", "acf_uid=1; junktoken", 1},
		{"空串", "", 0},
		{"仅分隔符", ";;; ", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := configs.ParseCookieFields(c.in)
			if len(got) != c.wantN {
				t.Fatalf("字段数=%d 期望=%d, got=%v", len(got), c.wantN, got)
			}
		})
	}
	// 值中的等号必须完整保留，否则续期合并会截断 JWT 类字段
	if m := configs.ParseCookieFields("acf_auth=a=b=c"); m["acf_auth"] != "a=b=c" {
		t.Fatalf("等号值被破坏: %q", m["acf_auth"])
	}
}

func TestMergeCookieFields(t *testing.T) {
	old := "acf_uid=111; acf_auth=old; dy_did=keepme"
	merged := configs.MergeCookieFields(old, map[string]string{"acf_auth": "new", "acf_stk": "stk"})

	fields := configs.ParseCookieFields(merged)
	if fields["acf_auth"] != "new" {
		t.Errorf("同名字段未被新值覆盖: %q", fields["acf_auth"])
	}
	if fields["acf_stk"] != "stk" {
		t.Errorf("新字段未写入: %q", fields["acf_stk"])
	}
	// 换票响应不含的身份/设备字段必须保留，否则 UID 显示与签名 did 会丢
	if fields["acf_uid"] != "111" || fields["dy_did"] != "keepme" {
		t.Errorf("未参与轮换的字段被丢弃: %v", fields)
	}
}

// 首次续期前 Cookies 中可能没有斗鱼条目（历史配置只存了 LTP0）
func TestMergeCookieFieldsFromEmpty(t *testing.T) {
	merged := configs.MergeCookieFields("", map[string]string{"acf_uid": "1"})
	if configs.ParseCookieFields(merged)["acf_uid"] != "1" {
		t.Fatalf("空旧值合并失败: %q", merged)
	}
}

func TestRenewSchedule(t *testing.T) {
	now := time.Unix(1700000000, 0)
	if got, want := dy.NextRenewAt(now)-now.Unix(), int64(3*24*time.Hour/time.Second); got != want {
		t.Errorf("续期间隔=%d 期望=%d（3 天）", got, want)
	}
	if got, want := dy.RenewBackoffAt(now)-now.Unix(), int64(6*time.Hour/time.Second); got != want {
		t.Errorf("退避间隔=%d 期望=%d（6 小时）", got, want)
	}
	// cookie 有效期约 6 天，续期点必须落在到期之前并留出一倍缓冲
	if dy.NextRenewAt(now) >= now.Add(6*24*time.Hour).Unix() {
		t.Error("续期点晚于 cookie 到期时间，会出现续期前就失效")
	}
}

// 合并后的 cookie 必须能被录制侧重新解析，且长度单调不缩（防止字段被静默吃掉）
func TestMergedCookieStaysParseable(t *testing.T) {
	fields := map[string]string{"acf_auth": "x.y.z", "acf_uid": "42"}
	merged := configs.MergeCookieFields("acf_auth=old", fields)
	if strings.Contains(merged, "old") {
		t.Errorf("旧值残留: %q", merged)
	}
	if len(configs.ParseCookieFields(merged)) != 2 {
		t.Errorf("合并结果无法解析为 2 个字段: %q", merged)
	}
}

// 续期结果必须按字段名有序写回：map 遍历随机，否则每次续期都把 config.yml 的 cookie 整行重排，
// 对把配置文件纳入 git 的用户产生无意义的持续 diff。
func TestMergeCookieFieldsIsDeterministic(t *testing.T) {
	old := "acf_uid=111; acf_auth=old; acf_stk=a; acf_ltkid=b; acf_ct=c"
	nf := map[string]string{"acf_auth": "new", "acf_username": "u", "acf_biz": "z"}
	first := configs.MergeCookieFields(old, nf)
	for i := 0; i < 50; i++ {
		if got := configs.MergeCookieFields(old, nf); got != first {
			t.Fatalf("第 %d 次拼接不一致:\n%s\n%s", i, first, got)
		}
	}
	pairs := strings.Split(first, "; ")
	for i, kv := range pairs {
		if i > 0 && kv < strings.Split(first, "; ")[i-1] {
			t.Fatalf("未按字段名升序: %s", first)
		}
	}
}

// 换票期间用户重新扫码（换了 LTP0）时必须放弃写入，否则旧账号的 acf_* 会覆盖新登录态。
func TestApplyDouyuRefreshResultSkipsStaleCredential(t *testing.T) {
	now := time.Unix(1893456000, 0)
	fields := map[string]string{"acf_uid": "1", "acf_auth": "old-account", "acf_stk": "s", "acf_ltkid": "l", "acf_username": "u", "acf_biz": "b", "acf_ct": "c"}

	t.Run("LTP0未变则正常写入", func(t *testing.T) {
		c := &configs.Config{
			Cookies:   map[string]string{dy.CookieHost: "acf_uid=1; acf_auth=stale"},
			DouyuAuth: configs.DouyuAuth{LTP0: "same.ticket", NeedRescan: true},
		}
		if !applyDouyuRefreshResult(c, "same.ticket", fields, now) {
			t.Fatal("期望写入成功")
		}
		if got := cookieValueOf(t, c.Cookies[dy.CookieHost], "acf_auth"); got != "old-account" {
			t.Errorf("新字段未写入, acf_auth=%q", got)
		}
		if c.DouyuAuth.NextRefreshAt != dy.NextRenewAt(now) {
			t.Errorf("未排下次续期时间: %d", c.DouyuAuth.NextRefreshAt)
		}
		if c.DouyuAuth.NeedRescan {
			t.Error("续期成功应清除失效提醒标记")
		}
		// 记下"这次真的续上了"，连续临时失败才会以此为基准升级为失效提醒
		if c.DouyuAuth.LastRenewSuccessAt != now.Unix() {
			t.Errorf("未记录本次成功续期时间: %d", c.DouyuAuth.LastRenewSuccessAt)
		}
	})

	t.Run("LTP0已被重新扫码替换则放弃写入", func(t *testing.T) {
		c := &configs.Config{
			Cookies:   map[string]string{dy.CookieHost: "acf_uid=999; acf_auth=new-account"},
			DouyuAuth: configs.DouyuAuth{LTP0: "new.ticket", NextRefreshAt: dy.NextRenewAt(now)},
		}
		if applyDouyuRefreshResult(c, "old.ticket", fields, now.Add(time.Hour)) {
			t.Fatal("期望放弃写入")
		}
		if got := cookieValueOf(t, c.Cookies[dy.CookieHost], "acf_auth"); got != "new-account" {
			t.Errorf("新登录态被旧结果覆盖: acf_auth=%q", got)
		}
		if got := cookieValueOf(t, c.Cookies[dy.CookieHost], "acf_uid"); got != "999" {
			t.Errorf("新账号 UID 被旧结果覆盖: acf_uid=%q", got)
		}
	})
}

func cookieValueOf(t *testing.T, cookieStr, name string) string {
	t.Helper()
	fields := configs.ParseCookieFields(cookieStr)
	v, ok := fields[name]
	if !ok {
		t.Fatalf("cookie 中缺少 %s: %s", name, cookieStr)
	}
	return v
}

// 扫码链跨 passport 域，Set-Cookie 里可能夹带 LTP0。主站不认这个字段，留在登录 cookie 里
// 只是让数月有效的长期凭证多一处落盘出口（/api/raw-config 只掩码 douyu_auth 字段），
// 因此长期凭证的唯一归属地必须是 DouyuAuth.LTP0。
func TestDouyuLoginCookieNeverCarriesLongTermTicket(t *testing.T) {
	now := time.Unix(1893456000, 0)

	scan := &configs.Config{}
	applyDouyuScanResult(scan, &dy.QRPollResult{
		Cookie: "acf_uid=1; acf_auth=fresh; LTP0=secret.ticket",
		LTP0:   "secret.ticket",
	}, now)
	if strings.Contains(scan.Cookies[dy.CookieHost], "LTP0") {
		t.Errorf("扫码结果 cookie 里残留长期票据: %q", scan.Cookies[dy.CookieHost])
	}
	if got := cookieValueOf(t, scan.Cookies[dy.CookieHost], "acf_auth"); got != "fresh" {
		t.Errorf("剥离长期票据时误删了登录字段: %q", got)
	}
	if scan.DouyuAuth.LTP0 != "secret.ticket" {
		t.Errorf("长期票据应保存在 DouyuAuth.LTP0, got %q", scan.DouyuAuth.LTP0)
	}

	merged := &configs.Config{
		Cookies:   map[string]string{dy.CookieHost: "acf_uid=1; acf_auth=old; LTP0=stale.ticket"},
		DouyuAuth: configs.DouyuAuth{LTP0: "ticket"},
	}
	if !applyDouyuRefreshResult(merged, "ticket", map[string]string{"acf_auth": "new", "LTP0": "exchanged.ticket"}, now) {
		t.Fatal("未写入换票结果")
	}
	if strings.Contains(merged.Cookies[dy.CookieHost], "LTP0") {
		t.Errorf("续期结果 cookie 里残留长期票据: %q", merged.Cookies[dy.CookieHost])
	}
}

// 扫码成功是"长期凭证唯一来源"，落盘时的归属判断错了，后台就会用错账号的票据续期。
func TestApplyDouyuScanResult(t *testing.T) {
	now := time.Unix(1893456000, 0)

	t.Run("带回LTP0则配对写入并排期", func(t *testing.T) {
		c := &configs.Config{
			Cookies: map[string]string{dy.CookieHost: "acf_uid=1; acf_auth=old"},
			DouyuAuth: configs.DouyuAuth{
				LTP0: "stale.ticket", DyDid: "stale-did", NeedRescan: true,
			},
		}
		applyDouyuScanResult(c, &dy.QRPollResult{
			Cookie: "acf_uid=1; acf_auth=new", LTP0: "fresh.ticket",
		}, now)
		if c.DouyuAuth.LTP0 != "fresh.ticket" {
			t.Errorf("LTP0 未更新: %q", c.DouyuAuth.LTP0)
		}
		// 设备号必须与 LTP0 同源配对：本次未下发就清空，不能留下上一轮的设备号与新票据配对
		if c.DouyuAuth.DyDid != "" {
			t.Errorf("未配对的旧设备号应清空: %q", c.DouyuAuth.DyDid)
		}
		if c.DouyuAuth.NextRefreshAt != dy.NextRenewAt(now) {
			t.Errorf("未排下次续期时间: %d", c.DouyuAuth.NextRefreshAt)
		}
		if c.DouyuAuth.NeedRescan || c.DouyuAuth.LastRenewSuccessAt != now.Unix() {
			t.Errorf("失效标记未清除或未记录新鲜登录态基准: %+v", c.DouyuAuth)
		}
		if got := cookieValueOf(t, c.Cookies[dy.CookieHost], "acf_auth"); got != "new" {
			t.Errorf("cookie 未整体替换: %q", got)
		}
	})

	// 本次扫码没回传 LTP0（异常分支）时，配置里残留的是上一次的票据。
	// 若换成了别的账号却继续留着，后台下次排期就会用旧账号换票、把旧账号 acf_* 合并进来，
	// 表现为面板显示新昵称却静默切回旧账号录制——宁可退化为"不能自动续期"也不能悄悄换人。
	t.Run("未带回LTP0且换了账号则放弃旧票据", func(t *testing.T) {
		c := &configs.Config{
			Cookies:   map[string]string{dy.CookieHost: "acf_uid=111; acf_auth=old"},
			DouyuAuth: configs.DouyuAuth{LTP0: "old-account.ticket", DyDid: "old-did", NextRefreshAt: dy.NextRenewAt(now)},
		}
		applyDouyuScanResult(c, &dy.QRPollResult{Cookie: "acf_uid=999; acf_auth=new"}, now)
		if c.DouyuAuth.LTP0 != "" || c.DouyuAuth.DyDid != "" {
			t.Errorf("归属存疑的旧长期凭证未清除: %+v", c.DouyuAuth)
		}
		// 票据虽然没了，但这次扫码确实换到了新鲜 cookie：日程照排、提醒照撤，
		// 只是 keeper 在 LTP0 为空时不会真的动作（见 refreshDouyuCookieIfDue 的门控）。
		if c.DouyuAuth.NextRefreshAt != dy.NextRenewAt(now) || c.DouyuAuth.LastRenewSuccessAt != now.Unix() {
			t.Errorf("未按新鲜登录态记录日程与基准: %+v", c.DouyuAuth)
		}
		if got := cookieValueOf(t, c.Cookies[dy.CookieHost], "acf_uid"); got != "999" {
			t.Errorf("新登录 cookie 被误伤: %q", got)
		}
	})

	t.Run("未带回LTP0但同账号则保留旧票据并撤销失效提醒", func(t *testing.T) {
		c := &configs.Config{
			Cookies: map[string]string{dy.CookieHost: "acf_uid=111; acf_auth=old"},
			DouyuAuth: configs.DouyuAuth{
				LTP0: "same-account.ticket", NextRefreshAt: dy.NextRenewAt(now), NeedRescan: true,
			},
		}
		applyDouyuScanResult(c, &dy.QRPollResult{Cookie: "acf_uid=111; acf_auth=new"}, now)
		if c.DouyuAuth.LTP0 != "same-account.ticket" {
			t.Errorf("同账号重扫不应丢掉自动续期能力: %q", c.DouyuAuth.LTP0)
		}
		// 这条是"红标死循环"的关键：用户按红字提示重扫后必须能解掉提醒，
		// 否则红标永远挂着、6 天后 cookie 自然过期且再无第二次提醒。
		if c.DouyuAuth.NeedRescan {
			t.Errorf("扫码成功后失效提醒未撤销: %+v", c.DouyuAuth)
		}
		if c.DouyuAuth.LastRenewSuccessAt != now.Unix() {
			t.Errorf("未按本次扫码记录新鲜登录态基准: %+v", c.DouyuAuth)
		}
	})

	t.Run("旧cookie无uid时不猜测归属，作废票据", func(t *testing.T) {
		c := &configs.Config{
			Cookies:   map[string]string{dy.CookieHost: "PHPSESSID=x"},
			DouyuAuth: configs.DouyuAuth{LTP0: "ticket"},
		}
		applyDouyuScanResult(c, &dy.QRPollResult{Cookie: "acf_uid=999; acf_auth=new"}, now)
		// 旧串里连 acf_uid 都没有，就无法证明这次登录与上一张票据同属一个账号；
		// 留着旧票据可能让后台下次排期用旧账号换票、把旧账号 acf_* 合并回来（静默换人录制）。
		if c.DouyuAuth.LTP0 != "" {
			t.Errorf("无法判断归属时应作废长期凭证: %q", c.DouyuAuth.LTP0)
		}
		if c.DouyuAuth.DyDid != "" {
			t.Errorf("票据作废时设备号必须一并清空: %q", c.DouyuAuth.DyDid)
		}
		// 但这次扫码得到的登录 cookie 本身要留下，不能因作废票据而退回匿名
		if !strings.Contains(c.Cookies[dy.CookieHost], "acf_uid=999") {
			t.Errorf("本次登录 cookie 未落盘: %q", c.Cookies[dy.CookieHost])
		}
	})
}

// 明文配置接口（"设置明文"页）整体序列化 YAML，json:"-" 挡不住它，
// 必须掩码斗鱼长期凭证：默认 rpc.bind 是 :8080 且接口无鉴权。
func TestGetRawConfigMasksDouyuSecrets(t *testing.T) {
	orig := configs.GetCurrentConfig()
	t.Cleanup(func() { configs.SetCurrentConfig(orig) })
	configs.SetCurrentConfig(&configs.Config{
		Cookies: map[string]string{dy.CookieHost: "acf_uid=42; acf_auth=cookie-secret"},
		DouyuAuth: configs.DouyuAuth{
			LTP0:  "ltp0-secret-ticket",
			DyDid: "did-secret-value",
		},
	})

	rec := httptest.NewRecorder()
	getRawConfig(rec, httptest.NewRequest(http.MethodGet, "/api/raw-config", nil))
	body := rec.Body.String()
	// 长期票据必须掩码：它数月有效、且只能由扫码引导，泄露即等于账号登录态被人长期接管
	for _, secret := range []string{"ltp0-secret-ticket", "did-secret-value"} {
		if strings.Contains(body, secret) {
			t.Fatalf("明文配置接口泄露了长期凭证 %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, douyuSecretMask) {
		t.Errorf("未按掩码输出，前端原样保存回去会把票据抹掉: %s", body)
	}
	// 主站 cookie 仍照旧可见：它约 6 天失效、可续期，且明文页是用户跨机备份登录态的唯一入口
	if !strings.Contains(body, "acf_auth=cookie-secret") {
		t.Errorf("主站 cookie 不应被掩码（会破坏配置备份用途）: %s", body)
	}
}

// 换票链在途可达分钟级（最多 8 跳 × 15s 超时），期间用户完全可能已经重扫成功或已清空凭证。
// 那一次"属于旧票据"的失败如果照常标红、改日程、推通知，用户刚做完系统要求的操作就收到假告警。
func TestHandleRenewInvalidIgnoresStaleCredential(t *testing.T) {
	orig := configs.GetCurrentConfig()
	t.Cleanup(func() { configs.SetCurrentConfig(orig); lastAlertedLTP0.Store("") })
	lastAlertedLTP0.Store("")

	const future int64 = 4102444800 // 2100 年：代表"刚扫码成功，已排到 3 天后"
	configs.SetCurrentConfig(&configs.Config{
		DouyuAuth: configs.DouyuAuth{LTP0: "new.ticket", NextRefreshAt: future},
	})

	handleRenewInvalid("old.ticket", errors.New("LTP0 可能已过期"))

	after := configs.GetCurrentConfig()
	if after.DouyuAuth.NeedRescan {
		t.Errorf("新登录态被旧凭证的失败标成需重新扫码: %+v", after.DouyuAuth)
	}
	if after.DouyuAuth.NextRefreshAt != future {
		t.Errorf("新日程被旧尝试的退避覆盖: %d != %d", after.DouyuAuth.NextRefreshAt, future)
	}
	if alerted, _ := lastAlertedLTP0.Load().(string); alerted == "old.ticket" {
		t.Errorf("为已作废的票据推送了重新扫码提醒")
	}
}

// 票据确实是当前这张时，才标失效、排退避，并记下"本进程已提醒过"用于去重
// （写盘失败时 NeedRescan 落不了盘，只靠它去重会每小时重复推送）。
func TestHandleRenewInvalidMarksRescanAndDedupsAlert(t *testing.T) {
	orig := configs.GetCurrentConfig()
	t.Cleanup(func() { configs.SetCurrentConfig(orig); lastAlertedLTP0.Store("") })
	lastAlertedLTP0.Store("")
	configs.SetCurrentConfig(&configs.Config{
		DouyuAuth: configs.DouyuAuth{LTP0: "cur.ticket"},
	})

	handleRenewInvalid("cur.ticket", errors.New("探针 error=-1"))

	after := configs.GetCurrentConfig()
	if !after.DouyuAuth.NeedRescan {
		t.Fatalf("当前票据续期失败应标记需重新扫码: %+v", after.DouyuAuth)
	}
	if after.DouyuAuth.NextRefreshAt <= time.Now().Unix() {
		t.Errorf("未安排退避后的续期时间: %d", after.DouyuAuth.NextRefreshAt)
	}
	if alerted, _ := lastAlertedLTP0.Load().(string); alerted != "cur.ticket" {
		t.Errorf("未记录本次提醒，写盘失败时将每重复推一次: %q", alerted)
	}
}

// 用户清空斗鱼 cookie 时 handler 会把 DouyuAuth 整个清零；此时在途尝试的退避不该再写回日程，
// 否则状态与"已无凭证"的事实不一致，面板会显示一条莫名的续期计划。
func TestSetNextRefreshAtIgnoresStaleOrClearedCredential(t *testing.T) {
	orig := configs.GetCurrentConfig()
	t.Cleanup(func() { configs.SetCurrentConfig(orig) })

	configs.SetCurrentConfig(&configs.Config{
		DouyuAuth: configs.DouyuAuth{LTP0: "new.ticket"},
	})
	setNextRefreshAt("old.ticket", 12345)
	if got := configs.GetCurrentConfig().DouyuAuth.NextRefreshAt; got != 0 {
		t.Errorf("旧尝试写回了新登录态的日程: %d", got)
	}

	configs.SetCurrentConfig(&configs.Config{}) // 凭证已清空
	setNextRefreshAt("old.ticket", 12345)
	if got := configs.GetCurrentConfig().DouyuAuth.NextRefreshAt; got != 0 {
		t.Errorf("无凭证配置被写入日程: %d", got)
	}

	configs.SetCurrentConfig(&configs.Config{
		DouyuAuth: configs.DouyuAuth{LTP0: "cur.ticket"},
	})
	setNextRefreshAt("cur.ticket", 12345)
	if got := configs.GetCurrentConfig().DouyuAuth.NextRefreshAt; got != 12345 {
		t.Errorf("当前票据的退避未写入: %d", got)
	}
}

// 两条换票链并发会把两次登录会话的字段合并落盘，而探针只分别验过各自字段集，混合结果未经验证。
// 现行设计里 keeper 只有一个 goroutine、且同步等本次换票返回，因此不存在并发链（见 refreshDouyuCookieIfDue 注释）。

// 配置写不进去（磁盘满/只读）时盘上日程不会推进，若没有内存退避兜底，
// 下一轮检查会立刻拿同一张票据再换一次票——safeAuth 是登录类接口，反复调用有风控风险。
func TestRenewScheduleOverrideKeepsBackoffWhenConfigUnwritable(t *testing.T) {
	clearRenewScheduleOverride()
	defer clearRenewScheduleOverride()

	cfg := &configs.Config{}
	cfg.DouyuAuth.NextRefreshAt = 100 // 早已到点
	if got := effectiveNextRefreshAt(cfg); got != 100 {
		t.Fatalf("无内存退避时应沿用盘上日程, got %d", got)
	}

	backoff := time.Now().Add(time.Hour).Unix()
	noteRenewSchedule(backoff)
	if got := effectiveNextRefreshAt(cfg); got != backoff {
		t.Fatalf("内存退避应覆盖过期的盘上日程, got %d", got)
	}
	if dy.ShouldRenewNow(effectiveNextRefreshAt(cfg), time.Now().Add(-time.Minute).Unix(), time.Now()) {
		t.Error("退避期间不应判定为到点")
	}

	// 盘上日程更晚时以盘上为准（例如用户重扫后排到了 3 天后）
	cfg.DouyuAuth.NextRefreshAt = backoff + 3600
	if got := effectiveNextRefreshAt(cfg); got != backoff+3600 {
		t.Fatalf("盘上日程更晚时应以盘上为准, got %d", got)
	}

	clearRenewScheduleOverride()
	if got := effectiveNextRefreshAt(cfg); got != backoff+3600 {
		t.Fatalf("清除退避后应回到盘上日程, got %d", got)
	}
}

// 扫码成功本身不作废内存退避：写盘失败时配置根本没落地，若 mutator 里顺手清掉退避，
// keeper 会绕过 6 小时退避每小时拿旧票据再打一次 safeAuth。清退避是调用方（写盘成功后）的职责。
func TestApplyDouyuScanResultKeepsRenewScheduleOverride(t *testing.T) {
	clearRenewScheduleOverride()
	defer clearRenewScheduleOverride()
	backoff := time.Now().Add(10 * 24 * time.Hour).Unix() // 故意晚于扫码排下的 3 天后日程，便于识别谁在生效
	noteRenewSchedule(backoff)

	c := &configs.Config{}
	applyDouyuScanResult(c, &dy.QRPollResult{Cookie: "acf_uid=1; acf_auth=x", LTP0: "new.ltp0"}, time.Now())
	if got := effectiveNextRefreshAt(c); got != backoff {
		t.Fatalf("闭包内不该作废内存退避（写盘可能失败）: got %d want %d", got, backoff)
	}
	// 调用方在写盘成功后清掉，之后交回盘上日程
	clearRenewScheduleOverride()
	if got := effectiveNextRefreshAt(c); got != c.DouyuAuth.NextRefreshAt {
		t.Fatalf("清除后应回到盘上日程: got %d want %d", got, c.DouyuAuth.NextRefreshAt)
	}
}
