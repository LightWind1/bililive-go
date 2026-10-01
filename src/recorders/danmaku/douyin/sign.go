package douyin

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0"

// browserVersion 用于 WebSocket URL 参数，与 userAgent 保持一致
const browserVersion = "5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

// getTtwidFromCookies 从 cookies 字符串中提取 ttwid
func getTtwidFromCookies(cookies string) string {
	for _, part := range strings.Split(cookies, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "ttwid=") {
			return strings.TrimPrefix(part, "ttwid=")
		}
	}
	return ""
}

// fetchTtwid 自动获取 ttwid
func fetchTtwid(logger *logrus.Entry) (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequest("GET", "https://live.douyin.com/", nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch live.douyin.com: %w", err)
	}
	defer resp.Body.Close()

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "ttwid" {
			logger.Info("自动获取 ttwid 成功")
			return cookie.Value, nil
		}
	}

	return "", fmt.Errorf("ttwid not found in response cookies")
}

// generateUserUniqueID 生成随机 user_unique_id
func generateUserUniqueID() string {
	return fmt.Sprintf("%d", rand.Int63n(9000000000000000000)+1000000000000000000)
}

// buildWSURL 构建 WebSocket URL（不含签名参数）
// 参考 DouyinLiveWebFetcher 的参数格式
func buildWSURL(roomID, ttwid, userUniqueID string) string {
	cursor := fmt.Sprintf("d-1_u-1_fh-7392091211001140287_t-%d_r-1", time.Now().UnixMilli())
	internalExt := fmt.Sprintf(
		"internal_src:dim|wss_push_room_id:%s|wss_push_did:%s|first_req_ms:%d|fetch_time:%d|seq:1|wss_info:0-%d-0-0|wrds_v:7392094459690748497",
		roomID, userUniqueID, time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(),
	)

	params := url.Values{}
	params.Set("app_name", "douyin_web")
	params.Set("version_code", "180800")
	params.Set("webcast_sdk_version", "1.0.14-beta.0")
	params.Set("update_version_code", "1.0.14-beta.0")
	params.Set("compress", "gzip")
	params.Set("device_platform", "web")
	params.Set("cookie_enabled", "true")
	params.Set("screen_width", "1920")
	params.Set("screen_height", "1080")
	params.Set("browser_language", "zh-CN")
	params.Set("browser_platform", "Win32")
	params.Set("browser_name", "Mozilla")
	params.Set("browser_version", browserVersion)
	params.Set("browser_online", "true")
	params.Set("tz_name", "Asia/Shanghai")
	params.Set("cursor", cursor)
	params.Set("internal_ext", internalExt)
	params.Set("host", "https://live.douyin.com")
	params.Set("aid", "6383")
	params.Set("live_id", "1")
	params.Set("did_rule", "3")
	params.Set("endpoint", "live_pc")
	params.Set("support_wrds", "1")
	params.Set("user_unique_id", userUniqueID)
	params.Set("im_path", "/webcast/im/fetch/")
	params.Set("identity", "audience")
	params.Set("need_persist_msg_count", "15")
	params.Set("insert_task_id", "")
	params.Set("live_reason", "")
	params.Set("room_id", roomID)
	params.Set("heartbeatDuration", "0")

	return "wss://webcast100-ws-web-lq.douyin.com/webcast/im/push/v2/?" + params.Encode()
}
