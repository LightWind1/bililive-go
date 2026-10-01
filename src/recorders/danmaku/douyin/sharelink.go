package douyin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// reOwnerShortID 锚定 room JSON 的 owner 对象提取 shortId（即主播账号级稳定的 web_rid）。
// 页面中其它对象（访客位等）也有 shortId:0，必须锚定 owner，不能盲取第一个。
// 兼容落地页两种形态：转义 JSON（\"owner\":{\"shortId\":N}）与普通 JSON。
var reOwnerShortID = regexp.MustCompile(`\\{0,2}"owner\\{0,2}":\{[^{}]*?\\{0,2}"shortId\\{0,2}":\s*(\d+)`)

// ResolveShareLongURL 将 v.douyin.com 分享短链口令（URL path 首段）转换为主播账号级稳定长链
// https://live.douyin.com/<web_rid>，供添加直播间时入库。
// 背景：短链 302 指向的 reflow 房间号是"分享那一刻场次"的快照，主播重新开播后指向死房间；
// reflow 落地页 owner.shortId 即网页版地址栏数字（web_rid），绑定账号不变，
// 转成长链后视频/弹幕/cookie 全链路自动获得与手工粘贴长链一致的跨场次行为。
// 解析失败返回 error，调用方应保留原短链入库（弹幕侧还有运行时逐跳解析兜底），不阻断添加流程。
func ResolveShareLongURL(ctx context.Context, code string) (string, error) {
	if !reShareCode.MatchString(code) {
		return "", fmt.Errorf("非法的分享短链口令: %q", code)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	next := "https://v.douyin.com/" + code + "/"
	// sawReflow：仅当跳转链中出现过 reflow 房间链接时，才信任落地页里的 owner 字段。
	// 防止失效/错误口令被抖音兜底导向直播分发首页等泛页面时，把页面中其它主播房间的 JSON 误提取为转换结果。
	sawReflow := false
	for hop := 0; hop < 6 && next != ""; hop++ {
		req, err := http.NewRequestWithContext(ctx, "GET", next, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc, uerr := resp.Location()
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if uerr != nil {
				return "", fmt.Errorf("跳转缺少 Location: %w", uerr)
			}
			if !isDouyinFamilyURL(loc) {
				return "", fmt.Errorf("短链跳转到非抖音域名: %s", loc.Host)
			}
			// 直接跳到 live.douyin.com/<web_rid> 形态：路径数字即答案
			if m := reWebRid.FindStringSubmatch(loc.String()); len(m) == 2 {
				return "https://live.douyin.com/" + m[1], nil
			}
			// reflow 落地页含 owner.shortId，抓页面提取；失败继续跟随跳转
			if reReflowRoomID.MatchString(loc.String()) {
				sawReflow = true
				if longURL, ferr := fetchOwnerLongURL(ctx, client, loc.String()); ferr == nil {
					return longURL, nil
				}
			}
			next = loc.String()
			continue
		}
		// 非跳转响应：仅在曾命中 reflow 链接时兜底解析落地页内容
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if rerr != nil {
			return "", rerr
		}
		if sawReflow {
			if m := reOwnerShortID.FindSubmatch(body); len(m) == 2 {
				return "https://live.douyin.com/" + string(m[1]), nil
			}
		}
		return "", fmt.Errorf("短链跳转链中未找到主播 web_rid (HTTP %d)", resp.StatusCode)
	}
	return "", fmt.Errorf("短链跳转超过最大跳数")
}

// fetchOwnerLongURL 抓取 reflow 落地页并锚定 owner 提取 shortId，返回长链 URL
func fetchOwnerLongURL(ctx context.Context, client *http.Client, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	m := reOwnerShortID.FindSubmatch(body)
	if len(m) != 2 {
		return "", fmt.Errorf("reflow 落地页未找到 owner.shortId")
	}
	return "https://live.douyin.com/" + string(m[1]), nil
}
