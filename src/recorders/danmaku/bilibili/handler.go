package bilibili

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// handleMessage 解析单条消息并分发到对应的回调
func (c *Client) handleMessage(data []byte) {
	// 使用正则快速提取 cmd，避免完整 JSON 解析
	// B站可能发送带冒号后缀的变体如 "DANMU_MSG:some_param"
	cmd := extractCMD(data)
	switch cmd {
	case "DANMU_MSG":
		if msg, ok := parseDanmaku(data); ok && c.onDanmaku != nil {
			c.onDanmaku(msg)
		}
	case "SEND_GIFT":
		if msg, ok := parseGift(data); ok && c.onGift != nil {
			c.onGift(msg)
		}
	case "SUPER_CHAT_MESSAGE":
		if msg, ok := parseSuperChat(data); ok && c.onSuperChat != nil {
			c.onSuperChat(msg)
		}
	case "GUARD_BUY":
		if msg, ok := parseGuardBuy(data); ok && c.onGuardBuy != nil {
			c.onGuardBuy(msg)
		}
	}
}

// extractCMD 从 JSON 数据中提取 cmd 字段，处理带冒号后缀的变体
func extractCMD(data []byte) string {
	matches := reCMD.FindSubmatch(data)
	if len(matches) < 2 {
		return ""
	}
	cmd := string(matches[1])
	// 去掉冒号后缀，如 "DANMU_MSG:param" -> "DANMU_MSG"
	if idx := strings.IndexByte(cmd, ':'); idx >= 0 {
		cmd = cmd[:idx]
	}
	return cmd
}

// parseDanmaku 解析弹幕消息
// info 数组结构：
// info[1]   = 弹幕文本
// info[2][0]= UID, info[2][1]= 用户名
// info[7]   = 舰队等级 (0=无, 1=总督, 2=提督, 3=舰长)
// info[3][0]= 勋章等级, info[3][1]= 勋章名, info[3][2]= UP主名
// info[0][4]= 时间戳, info[0][15].extra.color = 颜色
func parseDanmaku(data []byte) (DanmakuMsg, bool) {
	info := gjson.GetBytes(data, "info")
	if !info.Exists() {
		return DanmakuMsg{}, false
	}

	msg := DanmakuMsg{
		Content: info.Get("1").String(),
		UID:     info.Get("2.0").Int(),
		Uname:   info.Get("2.1").String(),
	}

	msg.GuardLevel = int(info.Get("7").Int())
	msg.Timestamp = info.Get("0.4").Int()

	// 勋章信息
	msg.MedalLevel = int(info.Get("3.0").Int())
	msg.MedalName = info.Get("3.1").String()
	msg.MedalUpName = info.Get("3.2").String()

	// 颜色（从 extra JSON 中提取）
	extraStr := info.Get("0.15.extra").String()
	if extraStr != "" {
		color := gjson.Get(extraStr, "color").Int()
		msg.Color = int(color)
	}

	// dm_v2 解析：新版弹幕格式，内容可能只存在于 dm_v2 中
	// 当 info[1] 为空或 dm_v2 存在时，优先使用 dm_v2 的内容
	if dmV2 := gjson.GetBytes(data, "dm_v2"); dmV2.Exists() {
		decoded, err := base64.StdEncoding.DecodeString(dmV2.String())
		if err == nil {
			// Content 是 protobuf 字段号 6（wire type 2）
			if content, ok := extractProtobufString(decoded, 6); ok && content != "" {
				msg.Content = content
			}
			// Color 是 protobuf 字段号 4（wire type 0, varint）
			if color, ok := extractProtobufUint32(decoded, 4); ok && color != 0 {
				msg.Color = int(color)
			}
		}
	}

	// dm_v2 解析后仍然没有内容，丢弃
	if msg.Content == "" {
		return DanmakuMsg{}, false
	}

	return msg, true
}

// parseGift 解析礼物消息
func parseGift(data []byte) (GiftMsg, bool) {
	giftData := gjson.GetBytes(data, "data")
	if !giftData.Exists() {
		return GiftMsg{}, false
	}

	var msg GiftMsg
	if err := json.Unmarshal([]byte(giftData.Raw), &msg); err != nil {
		return GiftMsg{}, false
	}

	if msg.GiftName == "" {
		return GiftMsg{}, false
	}
	if msg.Num < 1 {
		msg.Num = 1
	}

	return msg, true
}

// parseSuperChat 解析醒目留言（SC）
func parseSuperChat(data []byte) (SuperChatMsg, bool) {
	scData := gjson.GetBytes(data, "data")
	if !scData.Exists() {
		return SuperChatMsg{}, false
	}

	msg := SuperChatMsg{
		UID:     scData.Get("uid").Int(),
		Uname:   scData.Get("user_info.uname").String(),
		Message: scData.Get("message").String(),
		Price:   int(scData.Get("price").Int()),
	}

	if msg.Message == "" {
		return SuperChatMsg{}, false
	}

	return msg, true
}

// parseGuardBuy 解析舰长购买
func parseGuardBuy(data []byte) (GuardBuyMsg, bool) {
	guardData := gjson.GetBytes(data, "data")
	if !guardData.Exists() {
		return GuardBuyMsg{}, false
	}

	msg := GuardBuyMsg{
		UID:        guardData.Get("uid").Int(),
		Username:   guardData.Get("username").String(),
		GiftName:   guardData.Get("gift_name").String(),
		GuardLevel: int(guardData.Get("guard_level").Int()),
		Num:        int(guardData.Get("num").Int()),
		Price:      int(guardData.Get("price").Int()),
	}

	if msg.GiftName == "" {
		return GuardBuyMsg{}, false
	}
	if msg.Num < 1 {
		msg.Num = 1
	}

	return msg, true
}
