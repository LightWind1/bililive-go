package servers

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	dy "github.com/bililive-go/bililive-go/src/live/douyu"
	applog "github.com/bililive-go/bililive-go/src/log"
	"github.com/bililive-go/bililive-go/src/notify"
	bilisentry "github.com/bililive-go/bililive-go/src/pkg/sentry"
)

const (
	// douyuCheckInterval 后台仅做"是否到续期时间"的廉价检查频率，不触发网络请求。
	douyuCheckInterval = time.Hour
	// douyuFirstCheckDelay 启动后延迟首次检查，避开开机请求高峰并等待配置就绪。
	douyuFirstCheckDelay = 30 * time.Second
	// douyuSecretMask 是"设置明文"（/api/raw-config）里代替斗鱼长期凭证原文的占位符。
	// 明文接口整体序列化 YAML，会把 json:"-" 挡在 /api/config 之外的数月票据原样吐出，
	// 而默认 rpc.bind 是 :8080 且接口无鉴权；保存时识别该占位符即沿用原值。
	douyuSecretMask = "__BILILIVE_DOUYU_SECRET_HIDDEN__"
)

// StartDouyuCookieKeeper 启动斗鱼 cookie 后台自动续期循环。
// 只要存有 LTP0 长期凭证就按日程维护登录态（与当前是否有斗鱼房间无关，保证用户随时新增房间都能拿到新鲜 cookie）。
// 平时每小时只做一次时间比较，临近 cookie 到期才真正发起 safeAuth 换票；
// 换票成功字段级合并进 Cookies[www.douyu.com] 并热应用；凭证失效则去重推送"重新扫码"通知。
func StartDouyuCookieKeeper(ctx context.Context) {
	bilisentry.GoWithContext(ctx, func(ctx context.Context) {
		timer := time.NewTimer(douyuFirstCheckDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				refreshDouyuCookieIfDue(ctx)
				timer.Reset(douyuCheckInterval)
			}
		}
	})
}

// renewScheduleOverride 是配置写盘失败时替代盘上 NextRefreshAt 的内存到期时间（Unix 秒，0 表示无）。
// 写盘失败意味着盘上的"下次续期时间"没被推进，下一轮检查会立刻再换一次票；
// safeAuth 属登录类接口，反复调用有触发风控的风险，因此必须有个兜底的"这段时间别再试"。
var renewScheduleOverride atomic.Int64

// noteRenewSchedule 记录一次内存中的下次续期时间（写盘失败时调用）
func noteRenewSchedule(at int64) { renewScheduleOverride.Store(at) }

// clearRenewScheduleOverride 清除内存退避：一旦成功写盘或用户重扫，盘上日程重新可信
func clearRenewScheduleOverride() { renewScheduleOverride.Store(0) }

// effectiveNextRefreshAt 取"盘上日程"与"内存退避"中较晚的一个作为实际下次续期时间
func effectiveNextRefreshAt(cfg *configs.Config) int64 {
	next := cfg.DouyuAuth.NextRefreshAt
	if override := renewScheduleOverride.Load(); override > next {
		return override
	}
	return next
}

// refreshDouyuCookieIfDue 到点才续期：需存有 LTP0 且按 ShouldRenewNow 判定该动作。
// 换票链的串行性由调用方保证——只有 StartDouyuCookieKeeper 那一个 goroutine 会走到这里，
// 且它同步等本次换票返回后才重排下一次检查，因此不存在两条链并发合并字段的可能。
func refreshDouyuCookieIfDue(ctx context.Context) {
	cfg := configs.GetCurrentConfig()
	if cfg == nil || cfg.DouyuAuth.LTP0 == "" {
		return
	}
	// NextRefreshAt==0 视为"未知/立即续期"（如功能升级前已登录、没有该字段的历史配置）
	if !dy.ShouldRenewNow(effectiveNextRefreshAt(cfg), cfg.DouyuAuth.LastRenewSuccessAt, time.Now()) {
		return
	}
	doDouyuRefresh(ctx, cfg)
}

// doDouyuRefresh 执行一次 safeAuth 换票并落盘、热应用；同时更新下次续期时间与失效提醒状态。
func doDouyuRefresh(ctx context.Context, cfg *configs.Config) {
	now := time.Now()
	ltp0Used := cfg.DouyuAuth.LTP0
	fields, err := dy.RefreshLoginCookie(ctx, ltp0Used, cfg.DouyuAuth.DyDid)
	if err != nil {
		if errors.Is(err, dy.ErrLoginInvalid) {
			handleRenewInvalid(ltp0Used, err)
		} else {
			// 临时性错误（网络/HTTP 异常）：短退避重试，不打扰用户、不改失效标记。
			// 但"一直临时失败"和"永远续不上"在用户侧表现完全相同，所以距上次成功超过
			// 主站 cookie 整个有效期时升级为失效提醒，否则就是长期匿名录制而毫无线索。
			if dy.ShouldEscalateTempFailure(cfg.DouyuAuth.LastRenewSuccessAt, now) {
				handleRenewInvalid(ltp0Used, fmt.Errorf("斗鱼 cookie 已连续 %d 天未能续期（最近一次报错：%w）",
					int64(dy.RenewEscalateAfter()/(24*time.Hour)), err))
				return
			}
			setNextRefreshAt(ltp0Used, dy.RenewBackoffAt(now))
			applog.GetLogger().WithError(err).Warn("斗鱼 cookie 自动续期暂时失败（网络或接口异常），稍后自动重试")
		}
		return
	}
	skipped := false
	newCfg, err := configs.UpdateWithRetry(func(c *configs.Config) error {
		skipped = !applyDouyuRefreshResult(c, ltp0Used, fields, now)
		return nil
	}, 3, 10*time.Millisecond)
	if err != nil {
		// 票已换到但配置没写进去：updateCASImpl 在持久化失败时连内存都不替换，
		// 所以这份新 cookie 完全作废，盘上 NextRefreshAt 也仍是旧值，下一轮检查会立刻再换一次票。
		// safeAuth 属登录类接口，反复调用有触发风控的风险，故在内存里记一次退避。
		noteRenewSchedule(dy.RenewBackoffAt(now))
		warnScheduleWriteFailed(ltp0Used, err, "本次换到的新 cookie 与下次续期时间")
		return
	}
	// 配置已可写，内存退避不再需要，交回盘上日程
	clearRenewScheduleOverride()
	if skipped {
		applog.GetLogger().Info("斗鱼长期凭证在续期期间已被重新扫码替换，放弃写入本次换票结果（以新登录态为准）")
		return
	}
	applyCookiesToLives(ctx, newCfg, dy.CookieHost)
	noteRenewHealthy()
	next := dy.NextRenewAt(now)
	applog.GetLogger().Infof("斗鱼 cookie 自动续期成功，已热应用（更新字段数=%d，约 %d 小时后续期）", len(fields), (next-now.Unix())/3600)
}

// applyDouyuRefreshResult 把一次换票结果落到配置上，返回是否真正写入。
// ltp0Used 是本次换票实际使用的长期凭证：若配置里的 LTP0 已在其后被人换成新值
// （重新扫码、甚至换了账号），本次结果就属于上一轮登录态，继续合并会把旧账号的 acf_*
// 覆盖到新登录态上（表现为静默混号），此时放弃写入、以新登录态为准。
func applyDouyuRefreshResult(c *configs.Config, ltp0Used string, fields map[string]string, now time.Time) bool {
	if c.DouyuAuth.LTP0 != ltp0Used {
		return false
	}
	if c.Cookies == nil {
		c.Cookies = make(map[string]string)
	}
	// LTP0 只归 douyu_auth 一处存放；设备号由写回侧统一取舍（现有 cookie 已有则挡新值，一个都没有则补种）
	fresh := dy.ApplyDeviceIDPolicy(c.Cookies[dy.CookieHost], fields)
	c.Cookies[dy.CookieHost] = configs.DropCookieFields(configs.MergeCookieFields(c.Cookies[dy.CookieHost], fresh), "LTP0")
	c.DouyuAuth.NextRefreshAt = dy.NextRenewAt(now)
	c.DouyuAuth.NeedRescan = false              // 续期成功，清除失效提醒标记
	c.DouyuAuth.LastRenewSuccessAt = now.Unix() // 作为"连续临时失败该不该升级为提醒"的基准
	return true
}

// applyDouyuScanResult 把一次扫码登录的结果落到配置上（供 handler 在 CAS 闭包内调用）。
//
// 先无条件把"失效提醒"清掉并记下本次新鲜登录态的基准：扫码成功本身就意味着 Cookie 已翻新，
// 若只在"回传了 LTP0"时才清标记，那些"上游没回传 LTP0 且账号未变"的用户按红字提示重扫之后
// 红标永远解不掉（面板文案还承诺"登录后自动消失"），并且之后 6 天 cookie 自然过期时
// 再无第二次提醒——用户以为"我扫过了"，实际已退回匿名录制。
//
// LTP0 的归属才需要谨慎：本次没回传 LTP0 时配置里残留的是上一次的票据，
// 若换成了别的账号却继续留着，后台下次排期会用旧账号换票、把旧账号 acf_* 合并回来，
// 表现为面板显示新昵称却静默切回旧账号录制——宁可退化为"不能自动续期"也不能悄悄换人。
// 由于 LTP0 本身不记录归属，只能用 cookie 里的 acf_uid 做代理判断。
//
// 本函数只在 CAS 闭包内跑、除 c 之外不留副作用：写盘失败时配置根本没落地，
// 若在此处顺手清掉为"写盘失败"记的内存退避，反而会让 keeper 绕过 6 小时退避、
// 每小时拿旧票据再打一次 safeAuth。作废退避改由调用方在写盘成功后做。
func applyDouyuScanResult(c *configs.Config, res *dy.QRPollResult, now time.Time) {
	if c.Cookies == nil {
		c.Cookies = make(map[string]string)
	}
	oldUID := douyuCookieUid(c.Cookies[dy.CookieHost])
	// 扫码链跨 passport 域，Set-Cookie 里可能夹带 LTP0：主站并不认它，留在登录 cookie 里
	// 只是把数月有效的长期凭证多写到一个落盘出口（/api/raw-config 只掩码 douyu_auth 字段）。
	// 长期凭证的唯一归属地是 DouyuAuth.LTP0。
	loginCookie := configs.DropCookieFields(res.Cookie, "LTP0")
	c.Cookies[dy.CookieHost] = loginCookie
	newUID := douyuCookieUid(loginCookie)

	c.DouyuAuth.NeedRescan = false
	c.DouyuAuth.NextRefreshAt = dy.NextRenewAt(now)
	// 刚扫码得到的 cookie 与续期得到的一样是新鲜登录态，同样作为"临时失败升级"的基准
	c.DouyuAuth.LastRenewSuccessAt = now.Unix()

	if res.LTP0 != "" {
		c.DouyuAuth.LTP0 = res.LTP0
		// 设备号必须与 LTP0 同源配对（safeAuth 要的是签发那次的设备号），因此一并覆盖，
		// 未下发时清空而不是留下上一轮的设备号——实测缺设备号也能正常发权。
		c.DouyuAuth.DyDid = res.DyDid
	} else if oldUID == "" || newUID != oldUID {
		// 这次没回传长期凭证，又无法证明新登录态与上一张票据同属一个账号——要么是换了账号，
		// 要么是这次扫码根本解析不出 acf_uid（斗鱼改了字段名）。留着旧票据就可能让后台下次排期
		// 用旧账号换票、把旧账号的 acf_* 合并回来（面板显示新昵称却静默换人录制）。
		// 口径与 /api/cookies、"设置明文"两个手改入口保持一致：宁可放弃自动续期，也不能悄悄换人。
		if c.DouyuAuth.LTP0 != "" {
			applog.GetLogger().Warnf("斗鱼重新扫码未回传长期凭证，且无法确认与上一份登录态同属一个账号（uid %q -> %q），放弃上一轮的长期凭证（需再扫一次码才能启用自动续期）", oldUID, newUID)
		}
		c.DouyuAuth.LTP0 = ""
		c.DouyuAuth.DyDid = ""
	}
}

// lastAlertedLTP0 记录本进程已为哪张长期凭证推过"需重新扫码"提醒。
// NeedRescan 只能跨重启去重，一旦配置写盘失败（磁盘满/只读）它就形同不存在，
// 每小时重试会每小时推送一次；这个进程内标记补住该缺口（既不轰炸，也不至于完全静默）。
var lastAlertedLTP0 atomic.Value // string

// handleRenewInvalid 处理"凭证失效"（LTP0 过期等）：安排退避，并在首次失效时推送一次重新扫码通知。
// ltp0Used 必须是本次换票实际使用的那张票据：换票链在途可达分钟级，期间用户可能已经重扫
// （LTP0 换新）或已在面板清空斗鱼 cookie（LTP0 置空），此时这张票据的失败与当前登录态无关，
// 若照旧标红并推送，用户刚做完系统要求的操作就收到一条"登录已失效"的假告警。
func handleRenewInvalid(ltp0Used string, cause error) {
	shouldNotify := false
	stale := false
	// 先给个兜底退避：若每次尝试都在进入闭包前就版本冲突退出（mutator 压根没跑），
	// at 仍是 0，noteRenewSchedule(0) 等于"没有内存退避"，下一轮立刻拿同一张失效票据再打 safeAuth。
	at := dy.RenewBackoffAt(time.Now())
	if _, err := configs.UpdateWithRetry(func(c *configs.Config) error {
		stale = c.DouyuAuth.LTP0 != ltp0Used
		shouldNotify = !stale && !c.DouyuAuth.NeedRescan
		if stale {
			return nil
		}
		at = dy.RenewBackoffAt(time.Now())
		if c.DouyuAuth.NeedRescan {
			// NeedRescan 已经是置位的：说明这不是"可能误判"，而是上一次失效之后反复敲都不通。
			// 此时每小时重试同一张已判定失效的票据毫无收益，只会把 safeAuth 这种登录类接口
			// 敲成风控诱因，并把配置文件写成一小时一次的频率，故升档为长退避。
			at = dy.RenewInvalidRetryAt(time.Now())
		}
		c.DouyuAuth.NextRefreshAt = at
		c.DouyuAuth.NeedRescan = true
		return nil
	}, 3, 10*time.Millisecond); err != nil {
		if stale {
			// 票据已被替换/清空，连"配置写不进去"这条告警都不该记在它头上
			applog.GetLogger().WithError(err).Debug("斗鱼续期失败状态写入配置出错（本次所用长期凭证已作废，忽略）")
			return
		}
		applog.GetLogger().WithError(err).Warn("斗鱼续期失败状态写入配置出错")
		// NeedRescan/退避日程都没留住：不记内存退避的话，下一轮检查会立刻拿同一张已失效的票据再试
		noteRenewSchedule(at)
		warnScheduleWriteFailed(ltp0Used, err, "本次的失效标记与下次重试时间")
	}
	if stale {
		applog.GetLogger().Info("斗鱼本次续期所用长期凭证已被替换或清空，忽略该次失败结果（以当前登录态为准）")
		return
	}
	if alerted, _ := lastAlertedLTP0.Load().(string); alerted == ltp0Used {
		shouldNotify = false
	}
	applog.GetLogger().WithError(cause).Warn("斗鱼 cookie 自动续期失败，长期凭证 LTP0 可能已过期，需要重新扫码登录")
	if shouldNotify {
		lastAlertedLTP0.Store(ltp0Used)
		// 推送到独立 goroutine：通知渠道走外部网络（Telegram 在国内常连不上、SMTP 握手可达数十秒），
		// 而续期循环只有一个 goroutine，同步推送会让它在此期间完全停摆，
		// 连带把热应用、后续房间的到期检查一起卡住。
		bilisentry.Go(func() {
			notify.SendSystemAlert(
				"斗鱼登录已失效",
				"bililive-go 无法自动续期斗鱼登录 Cookie（长期凭证可能已过期），录制的斗鱼房间将退回匿名录制、可能被反复切断。请到 Web 界面重新扫码登录斗鱼。",
			)
		})
	}
}

// setNextRefreshAt 仅更新下次续期时间点（临时性失败的退避）。
// 同样要求 LTP0 仍是本次尝试所用那张，否则旧尝试的退避时间会覆盖掉用户重扫后排好的新日程。
func setNextRefreshAt(ltp0Used string, at int64) {
	if _, err := configs.UpdateWithRetry(func(c *configs.Config) error {
		if c.DouyuAuth.LTP0 != ltp0Used {
			return nil
		}
		c.DouyuAuth.NextRefreshAt = at
		return nil
	}, 3, 10*time.Millisecond); err != nil {
		applog.GetLogger().WithError(err).Warn("斗鱼续期退避时间写入配置失败")
		// 盘上日程没变，只能在内存里保住这次退避，否则下一轮立刻又用同一张票据重试
		noteRenewSchedule(at)
		warnScheduleWriteFailed(ltp0Used, err, "本次的退避时间")
	}
}

// scheduleWriteAlerted 按长期凭证去重"配置不可写"提醒，避免每小时重复推送同一条
var scheduleWriteAlerted atomic.Value // string

// warnScheduleWriteFailed 处理"配置写不进去"（磁盘满/只读/权限）：
// 只记一条 Warn 的话，用户侧表现是"cookie 再也没刷新过、也没有任何提示"——
// 续期结果与日程都留不住，必须显式告诉用户配置文件有问题。
// lost 说明这次具体没留住什么：三个调用点丢的东西并不一样（新 cookie / 失效标记 / 退避时间），
// 一律写成"续期结果没保存"会让用户以为票据已失效，跑去白扫一次码。
func warnScheduleWriteFailed(ltp0Used string, cause error, lost string) {
	if ltp0Used == "" {
		return
	}
	applog.GetLogger().WithError(cause).Warnf("斗鱼 cookie 续期状态写入配置失败，%s未落盘也未生效，请检查配置文件是否可写、磁盘是否已满", lost)
	if alerted, _ := scheduleWriteAlerted.Load().(string); alerted == ltp0Used {
		return
	}
	scheduleWriteAlerted.Store(ltp0Used)
	bilisentry.Go(func() {
		notify.SendSystemAlert(
			"斗鱼 cookie 续期状态无法保存",
			fmt.Sprintf("bililive-go 已能续期斗鱼登录 Cookie，但写入配置文件失败（%v）：%s没有保存，既没进内存也没落盘，等于这次白做。请检查配置文件是否可写、磁盘是否已满，解决后可在面板重新扫码。", cause, lost),
		)
	})
}

// noteRenewHealthy 在一次续期成功（已写盘、已热应用）后复位进程内的去重标记。
// 两张标记都按 LTP0 去重，而票据在整段有效期内根本不会变：若不复位，同一张票据只要
// 曾经"写盘失败提醒过一次"或"失效提醒过一次"，以后再出同样的问题就永远发不出第二条——
// 用户侧表现为"明明又坏了，却一句话都没有"。
func noteRenewHealthy() {
	lastAlertedLTP0.Store("")
	scheduleWriteAlerted.Store("")
}

// douyuCookieUid 取 cookie 串中的斗鱼登录用户 ID，用于判断两份 cookie 是否同一账号。
// 空串表示匿名/未登录（无 acf_uid）。
func douyuCookieUid(cookie string) string {
	return configs.ParseCookieFields(cookie)["acf_uid"]
}
