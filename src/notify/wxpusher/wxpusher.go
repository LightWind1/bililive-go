package wxpusher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

// WxPusherMessage WxPusher 消息结构
type WxPusherMessage struct {
	AppToken    string   `json:"appToken"`
	Content     string   `json:"content"`
	Summary     string   `json:"summary,omitempty"`
	ContentType int      `json:"contentType,omitempty"`
	UIDs        []string `json:"uids"`
}

// WxPusherResponse WxPusher 响应结构
type WxPusherResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// SendMessage 发送 WxPusher 消息
// appToken: 应用令牌（格式 AT_xxxx）
// uids: 接收者 UID 列表（格式 UID_xxxx）
// title: 消息标题（显示在微信消息列表）
// content: 消息内容
func SendMessage(appToken string, uids []string, title, content string) error {
	if len(uids) == 0 {
		return fmt.Errorf("wxpusher uids is empty")
	}

	msg := WxPusherMessage{
		AppToken:    appToken,
		Content:     content,
		Summary:     title,
		ContentType: 1, // 纯文本
		UIDs:        uids,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to encode wxpusher message: %w", err)
	}

	req, err := http.NewRequest("POST", "https://wxpusher.zjiecode.com/api/send/message", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create wxpusher request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send wxpusher request: %w", err)
	}
	defer resp.Body.Close()

	var respBody WxPusherResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return fmt.Errorf("failed to decode wxpusher response: %w", err)
	}

	if respBody.Code != 1000 {
		return fmt.Errorf("wxpusher error: code=%d, msg=%s", respBody.Code, respBody.Msg)
	}

	return nil
}
