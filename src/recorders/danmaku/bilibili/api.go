package bilibili

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/102.0.0.0 Safari/537.36"

var httpClient = &http.Client{Timeout: 10 * time.Second}

// mixinKeyEncTab Wbi 签名混淆表（64 元素）
var mixinKeyEncTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4,
	22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52,
}

// wbiKeys 缓存的 Wbi 签名密钥
var wbiKeys struct {
	sync.RWMutex
	mixin      string
	lastUpdate time.Time
}

// updateWbiKeys 从 B站 API 获取并更新 Wbi 密钥
func updateWbiKeys(cookie string) error {
	wbiKeys.RLock()
	if time.Since(wbiKeys.lastUpdate) < time.Hour && wbiKeys.mixin != "" {
		wbiKeys.RUnlock()
		return nil
	}
	wbiKeys.RUnlock()

	// HTTP 请求和解析在锁外执行，避免持锁阻塞其他读取
	req, err := http.NewRequest("GET", "https://api.bilibili.com/x/web-interface/nav", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var nav NavResponse
	if err := json.Unmarshal(body, &nav); err != nil {
		return err
	}
	// code 0 或 -101（未登录）都返回 wbi_img
	if nav.Code != 0 && nav.Code != -101 {
		return fmt.Errorf("nav API returned code %d", nav.Code)
	}

	imgHex := extractHex(nav.Data.WbiImg.ImgURL)
	subHex := extractHex(nav.Data.WbiImg.SubURL)
	if imgHex == "" || subHex == "" {
		return fmt.Errorf("failed to extract wbi keys from URLs")
	}

	wbi := imgHex + subHex
	mixin := make([]byte, 32)
	for i := 0; i < 32; i++ {
		mixin[i] = wbi[mixinKeyEncTab[i]]
	}

	// 仅在写入结果时持锁
	wbiKeys.Lock()
	wbiKeys.mixin = string(mixin)
	wbiKeys.lastUpdate = time.Now()
	wbiKeys.Unlock()
	return nil
}

// extractHex 从 URL 中提取文件名的 hex 部分
// 例如 "https://xxx/7cd084941338484aae1ad9425b84077c.png" → "7cd084941338484aae1ad9425b84077c"
func extractHex(rawURL string) string {
	idx := strings.LastIndex(rawURL, "/")
	if idx < 0 {
		return ""
	}
	name := rawURL[idx+1:]
	dotIdx := strings.LastIndex(name, ".")
	if dotIdx > 0 {
		name = name[:dotIdx]
	}
	return name
}

// signURL 对 URL 进行 Wbi 签名
func signURL(rawURL string, cookie string) (string, error) {
	if err := updateWbiKeys(cookie); err != nil {
		return rawURL, fmt.Errorf("update wbi keys: %w", err)
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, err
	}

	query := u.Query()
	query.Set("wts", strconv.FormatInt(time.Now().Unix(), 10))

	// 移除特殊字符
	params := url.Values{}
	for k, vs := range query {
		for _, v := range vs {
			cleaned := strings.NewReplacer("!", "", "'", "", "(", "", ")", "", "*", "").Replace(v)
			params.Set(k, cleaned)
		}
	}

	// 按 key 排序
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+params.Get(k))
	}
	wbiKeys.RLock()
	mixin := wbiKeys.mixin
	wbiKeys.RUnlock()
	signStr := strings.Join(parts, "&") + mixin

	hash := md5.Sum([]byte(signStr))
	query.Set("w_rid", fmt.Sprintf("%x", hash))
	u.RawQuery = query.Encode()

	return u.String(), nil
}

// GetRoomInit 将短号转换为真实 room_id
func GetRoomInit(roomID int) (int, error) {
	apiURL := fmt.Sprintf("https://api.live.bilibili.com/room/v1/Room/room_init?id=%d", roomID)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result RoomInitResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	if result.Code != 0 {
		return 0, fmt.Errorf("room_init API returned code %d", result.Code)
	}
	return result.Data.RoomID, nil
}

// GetDanmuInfo 获取弹幕服务器信息（token + host_list）
func GetDanmuInfo(roomID int, cookie string) (*DanmuInfoResponse, error) {
	apiURL := fmt.Sprintf("https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo?id=%d&type=0", roomID)

	signedURL, err := signURL(apiURL, cookie)
	if err != nil {
		return nil, fmt.Errorf("sign URL: %w", err)
	}

	req, err := http.NewRequest("GET", signedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result DanmuInfoResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("getDanmuInfo API returned code %d", result.Code)
	}
	return &result, nil
}

// GetUID 获取登录用户的 UID（cookie 为空时返回 0）
func GetUID(cookie string) (int, error) {
	if cookie == "" {
		return 0, nil
	}

	req, err := http.NewRequest("GET", "https://api.bilibili.com/x/web-interface/nav", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cookie", cookie)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result NavResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	return result.Data.Mid, nil
}
