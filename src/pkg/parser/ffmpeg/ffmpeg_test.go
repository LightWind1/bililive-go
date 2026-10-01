package ffmpeg

import (
	"strings"
	"testing"
)

// indexOf 返回 arg 在 args 中最后一次出现的位置，不存在时返回 -1
func indexOf(args []string, arg string) int {
	for i := len(args) - 1; i >= 0; i-- {
		if args[i] == arg {
			return i
		}
	}
	return -1
}

func countOccurrences(args []string, arg string) int {
	n := 0
	for _, a := range args {
		if a == arg {
			n++
		}
	}
	return n
}

func TestBuildExtraHeaders(t *testing.T) {
	t.Run("合并多个头并按 key 排序", func(t *testing.T) {
		got := buildExtraHeaders(map[string]string{
			"Cookie":     "a=1; b=2",
			"Referer":    "https://www.douyu.com/123",
			"User-Agent": "ua",
			"X-Tt-Req":   "req",
		})
		want := "Cookie: a=1; b=2\r\nX-Tt-Req: req"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	t.Run("UA 与 Referer 不重复下发", func(t *testing.T) {
		got := buildExtraHeaders(map[string]string{
			"Cookie":     "a=1",
			"Referer":    "r",
			"User-Agent": "ua",
		})
		if strings.Contains(got, "User-Agent") || strings.Contains(got, "Referer") {
			t.Fatalf("should exclude UA/Referer, got %q", got)
		}
	})

	t.Run("空值头跳过", func(t *testing.T) {
		got := buildExtraHeaders(map[string]string{"Cookie": "", "X-A": "1"})
		if got != "X-A: 1" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("只有 UA/Referer 时返回空串", func(t *testing.T) {
		if got := buildExtraHeaders(map[string]string{"User-Agent": "ua", "Referer": "r"}); got != "" {
			t.Fatalf("got %q", got)
		}
		if got := buildExtraHeaders(nil); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestBuildInputArgsHeadersBeforeInput(t *testing.T) {
	headers := map[string]string{
		"Cookie":     "acf_uid=1; acf_auth=2",
		"User-Agent": "custom-ua",
		"Referer":    "https://www.douyu.com/123456",
		"X-Extra":    "extra",
	}
	args := buildInputArgs(inputArgs{
		timeoutInUs: "5000000",
		inputURL:    "https://example.com/live.flv",
		headers:     headers,
		userAgent:   headers["User-Agent"],
		referer:     headers["Referer"],
		rateLimit:   true,
	})

	iIdx := indexOf(args, "-i")
	if iIdx < 0 {
		t.Fatalf("missing -i in %v", args)
	}
	// 回归点：请求头选项必须位于 -i 之前，否则 ffmpeg 视为输出选项静默丢弃，
	// 登录 cookie 不会随取流请求发出。
	for _, opt := range []string{"-user_agent", "-referer", "-headers"} {
		idx := indexOf(args, opt)
		if idx < 0 {
			t.Fatalf("missing %s in %v", opt, args)
		}
		if idx > iIdx {
			t.Fatalf("%s must appear before -i (idx=%d > i=%d)", opt, idx, iIdx)
		}
	}
	if idx := indexOf(args, "-rw_timeout"); idx > iIdx {
		t.Fatalf("-rw_timeout must appear before -i")
	}

	// -headers 只能出现一次，且多个头合并在同一个值里
	if n := countOccurrences(args, "-headers"); n != 1 {
		t.Fatalf("-headers should appear once, got %d in %v", n, args)
	}
	hIdx := indexOf(args, "-headers")
	value := args[hIdx+1]
	if !strings.Contains(value, "Cookie: acf_uid=1; acf_auth=2") {
		t.Fatalf("cookie missing from -headers value: %q", value)
	}
	if !strings.Contains(value, "X-Extra: extra") {
		t.Fatalf("custom header missing from -headers value: %q", value)
	}
	if strings.Contains(value, "\n") && !strings.Contains(value, "\r\n") {
		t.Fatalf("headers must be separated by CRLF, got %q", value)
	}
	// UA/Referer 由专属选项传递，不应在 -headers 中重复
	if strings.Contains(value, "User-Agent") || strings.Contains(value, "Referer") {
		t.Fatalf("UA/Referer duplicated in -headers: %q", value)
	}
}

func TestBuildInputArgsProxySkipsHeaders(t *testing.T) {
	args := buildInputArgs(inputArgs{
		timeoutInUs: "5000000",
		inputURL:    "http://127.0.0.1:1234/live.flv",
		headers:     map[string]string{"Cookie": "a=1", "User-Agent": "ua", "Referer": "r"},
		userAgent:   "ua",
		referer:     "r",
		useProxy:    true,
	})
	for _, opt := range []string{"-user_agent", "-referer", "-headers", "-re"} {
		if indexOf(args, opt) >= 0 {
			t.Fatalf("%s should not be present when using proxy: %v", opt, args)
		}
	}
	if args[len(args)-2] != "-i" {
		t.Fatalf("input should be last, got %v", args)
	}
}

func TestBuildInputArgsRateLimit(t *testing.T) {
	base := inputArgs{
		timeoutInUs: "1",
		inputURL:    "https://example.com/a.flv",
		userAgent:   "ua",
		referer:     "r",
		rateLimit:   true,
	}
	withLimit := buildInputArgs(base)
	if idx := indexOf(withLimit, "-re"); idx < 0 || idx > indexOf(withLimit, "-i") {
		t.Fatalf("-re expected before -i when rateLimit=true: %v", withLimit)
	}
	base.rateLimit = false
	if indexOf(buildInputArgs(base), "-re") >= 0 {
		t.Fatalf("-re unexpected when rateLimit=false")
	}
}
