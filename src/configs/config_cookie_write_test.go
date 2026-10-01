package configs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

// 手工编辑配置时常把斗鱼写成裸域或移动端域名，键不归一的话房间按标准 host 查不到 cookie，
// 既不报错也不显示在面板上，只是静默退回匿名录制（约 5 分钟被切断一次）。
func TestLoadNormalizesCookieHostAliases(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		wantN int
		want  string
	}{
		{"裸域补位", "cookies:\n  douyu.com: \"acf_uid=1\"\n", 1, "acf_uid=1"},
		{"移动端域名补位", "cookies:\n  m.douyu.com: \"acf_uid=2\"\n", 1, "acf_uid=2"},
		{"标准键优先于别名", "cookies:\n  douyu.com: \"stale\"\n  www.douyu.com: \"authoritative\"\n", 1, "authoritative"},
		{"空值别名不得挤掉标准键有效值", "cookies:\n  douyu.com: \"\"\n  www.douyu.com: \"keep\"\n", 1, "keep"},
		{"他平台键原样保留", "cookies:\n  www.huya.com: \"huya\"\n  live.bilibili.com: \"bili\"\n", 2, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := NewConfigWithBytes([]byte(c.yaml))
			assert.NoError(t, err)
			if len(cfg.Cookies) != c.wantN {
				t.Fatalf("归一后键数=%d 期望=%d, got=%v", len(cfg.Cookies), c.wantN, cfg.Cookies)
			}
			if c.want != "" && cfg.Cookies["www.douyu.com"] != c.want {
				t.Errorf("www.douyu.com=%q 期望 %q", cfg.Cookies["www.douyu.com"], c.want)
			}
			// 别名键不得残留：否则续期只更新标准键，别名那份会永远陈旧
			if _, ok := cfg.Cookies["douyu.com"]; ok {
				t.Errorf("别名键 douyu.com 未被合并: %v", cfg.Cookies)
			}
			if _, ok := cfg.Cookies["m.douyu.com"]; ok {
				t.Errorf("别名键 m.douyu.com 未被合并: %v", cfg.Cookies)
			}
		})
	}
}

// LTP0 是 passport 域的数月票据，只应存在 DouyuAuth.LTP0 一处。
// 混进 Cookies 就会从 /api/config 的 JSON 与 /api/raw-config 的 YAML 两个出口漏给浏览器。
func TestLoadStripsLongTermTicketFromCookieValues(t *testing.T) {
	cfg, err := NewConfigWithBytes([]byte("cookies:\n  www.douyu.com: \"acf_uid=1; LTP0=abc.def; acf_auth=x\"\n  live.bilibili.com: \"SESSDATA=keepme\"\n"))
	assert.NoError(t, err)
	got := cfg.Cookies["www.douyu.com"]
	if strings.Contains(got, "LTP0") {
		t.Errorf("LTP0 未从 cookie 串剔除: %q", got)
	}
	if !strings.Contains(got, "acf_uid=1") || !strings.Contains(got, "acf_auth=x") {
		t.Errorf("剔除时误伤了登录字段: %q", got)
	}
	// 未命中该字段的条目必须保持用户原样，避免产生无意义的配置 diff
	if cfg.Cookies["live.bilibili.com"] != "SESSDATA=keepme" {
		t.Errorf("他平台 cookie 被重排: %q", cfg.Cookies["live.bilibili.com"])
	}
}

// SetCookies 一批里同时提交别名与标准键时，必须以标准键为准（不能由 map 遍历顺序决定），
// 否则浏览器批量同步会随机覆盖掉刚写好的那份。
func TestSetCookiesAliasBatchIsDeterministic(t *testing.T) {
	orig := GetCurrentConfig()
	t.Cleanup(func() { SetCurrentConfig(orig) })
	base := NewConfig()
	SetCurrentConfig(base)

	for i := 0; i < 20; i++ {
		_, err := SetCookies(map[string]string{"douyu.com": "from-alias", "www.douyu.com": "from-standard"})
		assert.NoError(t, err)
		if got := GetCurrentConfig().Cookies["www.douyu.com"]; got != "from-standard" {
			t.Fatalf("第 %d 次：标准键被别名覆盖, got=%q", i, got)
		}
		if _, ok := GetCurrentConfig().Cookies["douyu.com"]; ok {
			t.Fatalf("别名键残留")
		}
		SetCurrentConfig(base)
	}
}

// Marshal 必须可回读、权限收紧到 0600，且不在目录里留下临时文件。
func TestMarshalWritesReadableConfigWithTightMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	c := NewConfig()
	c.File = path
	c.Cookies = map[string]string{"www.douyu.com": "acf_uid=1; acf_auth=x", "www.huya.com": "huya=1"}
	c.DouyuAuth = DouyuAuth{LTP0: "ticket.value", NextRefreshAt: 1893456000}
	assert.NoError(t, c.Marshal())

	b, err := os.ReadFile(path)
	assert.NoError(t, err)
	if !strings.Contains(string(b), "acf_uid=1") || !strings.Contains(string(b), "ticket.value") {
		t.Fatalf("内容未完整写入: %s", truncateForTest(string(b)))
	}
	// 原子写：同目录不得残留 .bililive-config-*.tmp
	entries, err := os.ReadDir(dir)
	assert.NoError(t, err)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".bililive-config-") {
			t.Errorf("临时文件残留: %s", e.Name())
		}
	}
	// Windows 下 Go 只能表达"只读/可写"，权限位恒为 0666，故仅在 POSIX 下断言收紧
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err == nil {
			if perm := info.Mode().Perm(); perm != 0600 {
				t.Errorf("权限未收紧: %v", perm)
			}
		}
	}
	// 回读必须等价（房间/cookie/票据都还在）
	reloaded, err := NewConfigWithFile(path)
	assert.NoError(t, err)
	if reloaded.Cookies["www.huya.com"] != "huya=1" {
		t.Errorf("回读后他平台 cookie 丢失: %v", reloaded.Cookies)
	}
	if reloaded.DouyuAuth.LTP0 != "ticket.value" {
		t.Errorf("回读后票据丢失: %q", reloaded.DouyuAuth.LTP0)
	}
}

// 已存在的宽权限配置文件在写回后必须被收紧（POSIX）；原地写分支同样要覆盖。
func TestWriteInPlaceFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	assert.NoError(t, os.WriteFile(path, []byte("interval: 20\n"), 0666))

	// 原地写分支（Docker 单文件挂载时 rename 会 EXDEV/EBUSY）
	c2 := NewConfig()
	c2.File = path
	assert.NoError(t, c2.writeInPlace([]byte("cookies:\n  www.douyu.com: \"in-place\"\n")))
	reloaded, err := NewConfigWithFile(path)
	assert.NoError(t, err)
	if reloaded.Cookies["www.douyu.com"] != "in-place" {
		t.Errorf("原地写内容未生效: %v", reloaded.Cookies)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		assert.NoError(t, err)
		if info.Mode().Perm() != 0600 {
			t.Errorf("已存在文件未收紧权限: %v", info.Mode().Perm())
		}
	}
}

// 写不进磁盘时不得把已有配置截断成半截 YAML（磁盘满对录制器是常规风险）
func TestMarshalFailureKeepsExistingConfigIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	assert.NoError(t, os.WriteFile(path, []byte("interval: 20\nout_put_path: ./\n"), 0600))

	c := NewConfig()
	c.File = filepath.Join(dir, "no-such-dir", "config.yml") // CreateTemp 与原地写都会失败
	assert.Error(t, c.Marshal())
	// 原文件必须仍然是完整可读的 YAML，且内容未被本次覆盖
	orig, err := os.ReadFile(path)
	assert.NoError(t, err)
	if !strings.Contains(string(orig), "interval: 20") {
		t.Errorf("原配置被破坏: %s", truncateForTest(string(orig)))
	}
}

func TestCloneForLogMasksSecretsWithoutMutatingOriginal(t *testing.T) {
	orig := NewConfig()
	orig.Cookies = map[string]string{"www.douyu.com": "acf_auth=secret-auth"}
	orig.DouyuAuth = DouyuAuth{LTP0: "secret-ticket", DyDid: "secret-did"}
	orig.SoopLiveAuth = SoopLiveAuth{Username: "u", Password: "secret-pw"}
	orig.Notify.Telegram.BotToken = "secret-bot"
	orig.Notify.Email.SenderPassword = "secret-smtp"
	orig.Notify.Ntfy.Token = "secret-ntfy"
	orig.Notify.Bark.DeviceKey = "secret-bark"
	orig.Notify.WxPusher.AppToken = "secret-wx"
	orig.OpenList.Password = "secret-webdav"
	orig.OpenList.Token = "secret-oplist-token"
	orig.Proxy.URL = "http://puser:ppass@proxy.example:3128"
	orig.Proxy.DownloadProxy = &ProxyEntry{URL: "http://duser:dpass@dl.example:3128"}

	cp := CloneForLog(orig)
	text := dumpForTest(cp)
	for _, s := range []string{"secret-auth", "secret-ticket", "secret-did", "secret-pw", "secret-bot", "secret-smtp", "secret-ntfy", "secret-bark", "secret-wx", "secret-webdav", "secret-oplist-token", "ppass", "dpass"} {
		if strings.Contains(text, s) {
			t.Errorf("日志副本泄露了 %q", s)
		}
	}
	// 浅拷贝陷阱：Proxy.DownloadProxy 是指针，改写它会污染正被业务使用的当前配置
	if orig.Proxy.DownloadProxy.URL != "http://duser:dpass@dl.example:3128" {
		t.Errorf("掩码污染了原配置: %q", orig.Proxy.DownloadProxy.URL)
	}
	if orig.Cookies["www.douyu.com"] != "acf_auth=secret-auth" {
		t.Errorf("掩码污染了原 cookie: %q", orig.Cookies["www.douyu.com"])
	}
	if orig.DouyuAuth.LTP0 != "secret-ticket" {
		t.Errorf("掩码污染了原票据: %q", orig.DouyuAuth.LTP0)
	}
	// 排障线索要留下：键与"配了没配"仍可见
	if cp.Cookies["www.douyu.com"] != LogSecretMask {
		t.Errorf("cookie 应替换为占位符, got=%q", cp.Cookies["www.douyu.com"])
	}
	if !strings.Contains(cp.Proxy.URL, "proxy.example:3128") {
		t.Errorf("代理 host 应保留以便确认配置: %q", cp.Proxy.URL)
	}
}

func truncateForTest(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func dumpForTest(c *Config) string {
	b, err := yaml.Marshal(c)
	if err != nil {
		return ""
	}
	return string(b)
}
