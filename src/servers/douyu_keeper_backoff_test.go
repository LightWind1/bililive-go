package servers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	dy "github.com/bililive-go/bililive-go/src/live/douyu"
)

// keeper 的前置闸门：没有长期凭证、或按日程还没到点时，必须"什么都不做"。
// 一旦闸门漏判，轻则每小时白打一次 safeAuth（登录类接口，反复调用有风控风险），
// 重则在完全没有票据的配置上写出一条莫名的续期日程。
func TestRefreshDouyuCookieIfDueGate(t *testing.T) {
	orig := configs.GetCurrentConfig()
	t.Cleanup(func() { configs.SetCurrentConfig(orig) })
	clearRenewScheduleOverride()
	t.Cleanup(clearRenewScheduleOverride)

	cases := []struct {
		name string
		auth configs.DouyuAuth
	}{
		{"没有长期凭证（从未扫码）", configs.DouyuAuth{}},
		{"票据在但日程未到", configs.DouyuAuth{LTP0: "ticket.value", NextRefreshAt: time.Now().Add(48 * time.Hour).Unix(), LastRenewSuccessAt: time.Now().Add(-time.Hour).Unix()}},
		{"票据在、处于 6 小时退避期内", configs.DouyuAuth{LTP0: "ticket.value", NextRefreshAt: time.Now().Add(30 * time.Minute).Unix(), LastRenewSuccessAt: time.Now().Add(-time.Hour).Unix()}},
	}
	ctx := context.Background()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := writableConfig(t)
			cfg.Cookies = map[string]string{dy.CookieHost: "acf_uid=111; acf_auth=keep"}
			cfg.DouyuAuth = c.auth
			configs.SetCurrentConfig(cfg)
			version, cookieBefore := cfg.Version, cfg.Cookies[dy.CookieHost]

			refreshDouyuCookieIfDue(ctx)

			after := configs.GetCurrentConfig()
			if after.Version != version {
				t.Errorf("闸门内不该提交任何配置更新（版本被推进说明走完了换票写回）: %d -> %d", version, after.Version)
			}
			if after.Cookies[dy.CookieHost] != cookieBefore {
				t.Errorf("登录 cookie 被动过: %q", after.Cookies[dy.CookieHost])
			}
			if after.DouyuAuth.NextRefreshAt != c.auth.NextRefreshAt || after.DouyuAuth.NeedRescan {
				t.Errorf("续期日程/失效标记被改写: %+v", after.DouyuAuth)
			}
		})
	}

	// 配置尚未加载（启动早期）时同样必须安静返回，而不是 panic
	configs.SetCurrentConfig(nil)
	refreshDouyuCookieIfDue(ctx)
}

// 已经判定过失效之后，重试必须升档到 24 小时（而不是停在 6 小时）：
// 否则一张已判死的票据会被每天敲 4 次，既最容易触发风控，也把配置文件写成每天 4 次重写。
func TestHandleRenewInvalidEscalatesBackoffAfterFirstJudgement(t *testing.T) {
	orig := configs.GetCurrentConfig()
	t.Cleanup(func() {
		configs.SetCurrentConfig(orig)
		lastAlertedLTP0.Store("")
		clearRenewScheduleOverride()
	})
	clearRenewScheduleOverride()
	const ticket = "dead.ticket"

	// 第一次判定失效：6 小时档（给"可能只是风控误判"留自愈窗口）
	configs.SetCurrentConfig(&configs.Config{DouyuAuth: configs.DouyuAuth{LTP0: ticket}})
	lastAlertedLTP0.Store("")
	handleRenewInvalid(ticket, errors.New(dy.ErrLoginInvalid.Error()))
	first := configs.GetCurrentConfig().DouyuAuth
	if delta := first.NextRefreshAt - time.Now().Unix(); delta < 5*3600 || delta > 7*3600 {
		t.Fatalf("首次失效应排 6 小时档: delta=%ds", delta)
	}
	if !first.NeedRescan {
		t.Fatal("首次失效应置需重新扫码标记")
	}

	// 第二次：NeedRescan 已置位 ⇒ 升档
	lastAlertedLTP0.Store("")
	handleRenewInvalid(ticket, errors.New(dy.ErrLoginInvalid.Error()))
	second := configs.GetCurrentConfig().DouyuAuth
	if delta := second.NextRefreshAt - time.Now().Unix(); delta < 23*3600 || delta > 25*3600 {
		t.Errorf("重复判定失效应升到 24 小时档: delta=%ds", delta)
	}

	// 退避日程本身必须能挡住 ShouldRenewNow 的两个提前理由，否则等于每小时打一次
	if dy.ShouldRenewNow(second.NextRefreshAt, second.LastRenewSuccessAt, time.Now()) {
		t.Errorf("24 小时退避期内仍被判为到点")
	}
	// 而到点之后（25 小时后）应当重新放行，不至于永久停摆
	if !dy.ShouldRenewNow(second.NextRefreshAt, second.LastRenewSuccessAt, time.Now().Add(25*time.Hour)) {
		t.Errorf("退避到期后仍未放行续期")
	}
}

// 健康的一次续期/扫码必须复位进程内的提醒去重标记：票据在整段有效期内不会变，
// 若不复位，同一张票据曾经提醒过一次之后就再也发不出第二条。
func TestNoteRenewHealthyReopensAlertDedup(t *testing.T) {
	lastAlertedLTP0.Store("some.ticket")
	scheduleWriteAlerted.Store("some.ticket")

	noteRenewHealthy()

	if v, _ := lastAlertedLTP0.Load().(string); v != "" {
		t.Errorf("失效提醒去重标记未复位: %q", v)
	}
	if v, _ := scheduleWriteAlerted.Load().(string); v != "" {
		t.Errorf("写盘失败提醒去重标记未复位: %q", v)
	}
}
