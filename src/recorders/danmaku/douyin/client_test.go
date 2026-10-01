package douyin

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// TestPhase1Connection 测试不带签名的连接（需要手动提供 roomID 参数）
func TestPhase1Connection(t *testing.T) {
	t.Skip("需要通过环境变量测试，请使用 TestWithCookies")
}

// TestWithCookies 带完整 cookies 测试
// DOUYIN_ROOM=400604888272 DOUYIN_COOKIES="ttwid=xxx; ..." go test -v -timeout 60s -run TestWithCookies
func TestWithCookies(t *testing.T) {
	roomID := os.Getenv("DOUYIN_ROOM")
	cookies := os.Getenv("DOUYIN_COOKIES")
	if roomID == "" || cookies == "" {
		t.Skip("请设置环境变量: DOUYIN_ROOM=<roomID> DOUYIN_COOKIES=<cookies>")
	}
	testConnection(t, roomID, cookies)
}

func testConnection(t *testing.T, roomID, cookies string) {
	logger := logrus.New().WithField("test", "douyin")

	// Step 1: 获取 ttwid
	t.Log("Step 1: 获取 ttwid...")
	ttwid := getTtwidFromCookies(cookies)
	if ttwid == "" {
		var err error
		ttwid, err = fetchTtwid(logger)
		if err != nil {
			t.Fatalf("获取 ttwid 失败: %v", err)
		}
	}
	t.Logf("ttwid: %s...", ttwid[:min(20, len(ttwid))])

	// Step 2: 获取真实 roomId
	t.Log("Step 2: 获取真实 roomId...")
	realRoomID, err := fetchRealRoomID(roomID, cookies, logger)
	if err != nil {
		t.Logf("获取真实 roomId 失败，使用原始值: %v", err)
		realRoomID = roomID
	}
	t.Logf("realRoomID: %s", realRoomID)

	// Step 3: 生成 user_unique_id
	userUniqueID := generateUserUniqueID()
	t.Logf("userUniqueID: %s", userUniqueID)

	// Step 4: 构建 URL
	wsURL := buildWSURL(realRoomID, ttwid, userUniqueID)

	// Step 5: 生成 signature
	t.Log("Step 5: 生成 signature...")
	signature, err := generateSignature(wsURL, logger)
	if err != nil {
		t.Fatalf("生成 signature 失败: %v", err)
	}
	t.Logf("signature: %s...", signature[:min(30, len(signature))])
	wsURL += "&signature=" + signature

	t.Logf("WebSocket URL (前120字符): %s...", wsURL[:min(120, len(wsURL))])

	// Step 6: 尝试连接
	t.Log("Step 6: 尝试 WebSocket 连接...")

	header := http.Header{}
	header.Set("User-Agent", userAgent)
	header.Set("Origin", "https://live.douyin.com")
	if cookies != "" {
		header.Set("Cookie", cookies)
	} else {
		header.Set("Cookie", "ttwid="+ttwid)
	}

	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, dialErr := dialer.Dial(wsURL, header)

	if resp != nil {
		t.Logf("WebSocket 响应状态码: %d", resp.StatusCode)
		t.Logf("Handshake-Msg: %s", resp.Header.Get("Handshake-Msg"))
		t.Logf("Handshake-Status: %s", resp.Header.Get("Handshake-Status"))
		for k, v := range resp.Header {
			if k != "Server" && k != "Date" && k != "Via" && k != "Server-Timing" &&
				k != "X-Dsa-Origin-Status" && k != "X-Dsa-Trace-Id" && k != "X-Request-Ip" &&
				k != "X-Tt-Logid" && k != "X-Tt-Trace-Host" && k != "X-Tt-Trace-Id" && k != "X-Tt-Trace-Tag" {
				t.Logf("  %s: %v", k, v)
			}
		}
		resp.Body.Close()
	}

	if dialErr != nil {
		t.Fatalf("WebSocket 连接失败: %v", dialErr)
	}
	defer conn.Close()

	t.Log("连接成功！等待消息...")
	msgCount := 0
	timer := time.After(15 * time.Second)

	for {
		select {
		case <-timer:
			t.Logf("测试完成！共收到 %d 条消息", msgCount)
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, message, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Logf("读取消息结束 (已收到 %d 条): %v", msgCount, readErr)
			return
		}

		msgCount++
		frame := &PushFrame{}
		if parseErr := frame.Unmarshal(message); parseErr != nil {
			t.Logf("消息 #%d: 解析 PushFrame 失败: %v", msgCount, parseErr)
			continue
		}

		t.Logf("消息 #%d: seqId=%d, logId=%d, service=%d, payloadType=%q, payloadLen=%d",
			msgCount, frame.SeqId, frame.LogId, frame.Service, frame.PayloadType, len(frame.Payload))

		if len(frame.Payload) > 0 {
			// 打印前 50 字节的 hex
			hexDump := fmt.Sprintf("%x", frame.Payload[:min(50, len(frame.Payload))])
			t.Logf("  payload hex: %s", hexDump)

			decompressed, gzipErr := gzipDecompress(frame.Payload)
			if gzipErr != nil {
				t.Logf("  GZIP 解压失败: %v (跳过)", gzipErr)
				continue
			}
			resp := &Response{}
			if respErr := resp.Unmarshal(decompressed); respErr != nil {
				t.Logf("  解析 Response 失败: %v", respErr)
				continue
			}
			t.Logf("  Response: %d 条消息, needAck=%v, heartbeatDuration=%d",
				len(resp.MessagesList), resp.NeedAck, resp.HeartbeatDuration)
			for i, msg := range resp.MessagesList {
				t.Logf("    消息[%d]: method=%s, payloadLen=%d", i, msg.Method, len(msg.Payload))
				// 对 ChatMessage 打印原始 hex 帮助调试
				if msg.Method == "WebcastChatMessage" && len(msg.Payload) > 0 {
					hexDump := fmt.Sprintf("%x", msg.Payload)
					t.Logf("      ChatMessage hex (%d bytes): %s", len(msg.Payload), hexDump[:min(400, len(hexDump))])
				}
			}
		}
	}
}

// TestShareLinkParsers 短链解析纯函数单测（不依赖网络）
func TestShareLinkParsers(t *testing.T) {
	if !isNumericRoomID("400604888272") || isNumericRoomID("") || isNumericRoomID("gLXTJdPjW-Y") || isNumericRoomID("12a3") {
		t.Fatal("isNumericRoomID 判定错误")
	}
	// reflow 路径与 room_id query 均为真实 roomId
	if id := roomIDFromRedirectURL("https://webcast.amemv.com/douyin/webcast/reflow/7688535243871046409?u_code=x"); id != "7688535243871046409" {
		t.Fatalf("reflow 提取失败: %q", id)
	}
	if id := roomIDFromRedirectURL("https://live.douyin.com/?room_id=123&x=1"); id != "123" {
		t.Fatalf("room_id query 提取失败: %q", id)
	}
	// live.douyin.com/<web_rid> 不在 redirect 提取阶段直接返回，避免误把 web_rid 当 roomId
	if id := roomIDFromRedirectURL("https://live.douyin.com/400604888272"); id != "" {
		t.Fatalf("web_rid 不应被当作真实 roomId: %q", id)
	}
	// roomIdStr 转义/非转义两种形态
	for _, s := range []string{
		`"roomIdStr\":\"7688535243871046409\"`,
		`"roomIdStr":"7688535243871046409"`,
	} {
		m := reRoomIDStr.FindStringSubmatch(s)
		if len(m) != 2 || m[1] != "7688535243871046409" {
			t.Fatalf("roomIdStr 提取失败: %s", s)
		}
	}
	// 跳转域名白名单
	type hostCase struct {
		raw  string
		want bool
	}
	for _, c := range []hostCase{
		{"https://webcast.amemv.com/douyin/webcast/reflow/1", true},
		{"https://v.douyin.com/abc/", true},
		{"https://live.douyin.com/123", true},
		{"https://example.com/douyin.com/", false},
		{"https://evil.douyin.com.attacker.net/x", false},
		{"http://snssdk.com/x", true},
		{"file:///etc/passwd", false},
	} {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := isDouyinFamilyURL(u); got != c.want {
			t.Fatalf("isDouyinFamilyURL(%s)=%v want %v", c.raw, got, c.want)
		}
	}
	// 口令白名单拒绝路径穿越/非法字符
	if reShareCode.MatchString("../x") || reShareCode.MatchString("a/b") || reShareCode.MatchString("") {
		t.Fatal("reShareCode 白名单过松")
	}
	if !reShareCode.MatchString("gLXTJdPjW-Y") {
		t.Fatal("reShareCode 拒绝了合法口令")
	}
}

// TestOwnerShortIDRegex 验证 owner 锚定提取，不会被访客位 shortId:0 干扰
func TestOwnerShortIDRegex(t *testing.T) {
	escaped := `xx],"owner":{"id\":4336069736662504,\"shortId\":3511285081,\"nickname\":\"半只笨猪`
	// 构造更接近真实落地页的全转义片段
	escapedFull := `\"owner\":{\"id\":4336069736662504,\"shortId\":3511285081,\"nickname\":`
	plain := `"owner":{"id":4336069736662504,"shortId":3511285081,"nickname":"x"}`
	guest := `{"guest":{"shortId":0}}` + escapedFull
	for _, s := range []string{escapedFull, plain, guest} {
		m := reOwnerShortID.FindStringSubmatch(s)
		if len(m) != 2 || m[1] != "3511285081" {
			t.Fatalf("owner.shortId 提取失败: %s -> %v", s, m)
		}
	}
	if reOwnerShortID.MatchString(escaped) {
		t.Log("注意: 混入未转义片段仍要求匹配失败与否取决于结构，此处仅记录")
	}
	if m := reOwnerShortID.FindStringSubmatch(`{"owner":{"id":1,"other":{"shortId":9}},"x":1}`); m != nil {
		t.Fatalf("不应跨出 owner 对象匹配: %v", m)
	}
}
