package servers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// decodePollData 解出轮询响应的 data 字段。
func decodePollData(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var parsed struct {
		Data map[string]interface{} `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(body, &parsed))
	return parsed.Data
}

func TestAttachBilibiliLoginCookiesOnSuccess(t *testing.T) {
	body := []byte(`{"code":0,"message":"OK","data":{"code":0,"message":"成功","url":"https://www.bilibili.com","refresh_token":"rt-1","timestamp":1700000000}}`)

	out := attachBilibiliLoginCookies(body, []*http.Cookie{
		{Name: "SESSDATA", Value: "sd-value"},
		{Name: "bili_jct", Value: "jct-value"},
		{Name: "DedeUserID", Value: "12345"},
		{Name: "sid", Value: "sid-value"},
		{Name: "buvid3", Value: "tracking-should-be-dropped"},
	})

	data := decodePollData(t, out)
	cookies, ok := data["cookies"].(map[string]interface{})
	assert.True(t, ok, "登录成功时应回传 data.cookies")
	assert.Equal(t, "sd-value", cookies["SESSDATA"])
	assert.Equal(t, "jct-value", cookies["bili_jct"])
	assert.Equal(t, "12345", cookies["DedeUserID"])
	assert.NotContains(t, cookies, "buvid3", "埋点字段不应写入配置")

	// 上游原有字段必须保留：这里曾是结构体重新序列化，会静默丢掉 refresh_token/timestamp
	assert.Equal(t, "rt-1", data["refresh_token"])
	assert.NotEmpty(t, data["timestamp"])
}

func TestAttachBilibiliLoginCookiesKeepsPendingResponse(t *testing.T) {
	body := []byte(`{"code":0,"message":"OK","ttl":1,"data":{"code":86101,"message":"未扫码","url":"","refresh_token":"","timestamp":0}}`)

	out := attachBilibiliLoginCookies(body, []*http.Cookie{{Name: "SESSDATA", Value: "leak"}})

	assert.Equal(t, string(body), string(out), "未扫码时应原样透传")
	assert.NotContains(t, decodePollData(t, out), "cookies")
}

func TestAttachBilibiliLoginCookiesPrefersSetCookieOverURLQuery(t *testing.T) {
	body := []byte(`{"code":0,"data":{"code":0,"url":"https://www.bilibili.com?SESSDATA=from-url&sid=from-url"}}`)

	out := attachBilibiliLoginCookies(body, []*http.Cookie{{Name: "SESSDATA", Value: "from-header"}})

	cookies := decodePollData(t, out)["cookies"].(map[string]interface{})
	assert.Equal(t, "from-header", cookies["SESSDATA"])
	assert.Equal(t, "from-url", cookies["sid"])
}

func TestAttachBilibiliLoginCookiesPreservesLargeIntegers(t *testing.T) {
	// 9007199254740993 = 2^53+1，已超出 float64 的精确整数范围：
	// 若按默认 float64 解析再序列化，会被静默舍入成 9007199254740992
	body := []byte(`{"code":0,"data":{"code":0,"url":"https://www.bilibili.com","bp_offset":9007199254740993}}`)

	out := attachBilibiliLoginCookies(body, []*http.Cookie{{Name: "SESSDATA", Value: "sd"}})

	assert.Contains(t, string(out), `"bp_offset":9007199254740993`, "大整数必须逐字节保留")
}
