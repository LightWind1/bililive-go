package configs

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// 本文件守住弹幕配置两条极易被改回去的不变量：
//  1. yaml.Unmarshal 对非 nil 指针是"顺着指针原地写入"，而 NewConfig() 只是把包级
//     defaultConfig 按值拷贝出来，弹幕的 *int/*bool 仍指向 defaultDanmakuConfig。
//     不先换成独立副本，任何一份用户配置都会永久改掉包级默认值，且后续新建的每份
//     配置都带着上一份的值。
//  2. opacity 语义升级（旧值渲染无效）带来的 128→255 迁移只能做一次，否则新版面板
//     里用户明确选定的 128 一存盘就丢。

func assertPackageDanmakuDefaultsIntact(t *testing.T, after string) {
	t.Helper()
	if got := defaultDanmakuConfig.FontSize; got != 36 {
		t.Fatalf("%s: 包级默认 font_size = %d, 期望 36", after, got)
	}
	for _, f := range []struct {
		name string
		got  *int
		want int
	}{
		{"outline", defaultDanmakuConfig.Outline, 1},
		{"opacity", defaultDanmakuConfig.Opacity, 255},
	} {
		if f.got == nil || *f.got != f.want {
			t.Fatalf("%s: 包级默认 %s 被改成了 %v, 期望 %d", after, f.name, derefInt(f.got), f.want)
		}
	}
	for _, f := range []struct {
		name string
		got  *bool
	}{
		{"record_gift", defaultDanmakuConfig.RecordGift},
		{"record_douyu_gift", defaultDanmakuConfig.RecordDouyuGift},
		{"record_douyin_gift", defaultDanmakuConfig.RecordDouyinGift},
		{"record_guard", defaultDanmakuConfig.RecordGuard},
		{"record_super_chat", defaultDanmakuConfig.RecordSuperChat},
	} {
		if f.got == nil || !*f.got {
			t.Fatalf("%s: 包级默认 %s 被改成了 %v, 期望 true", after, f.name, derefBool(f.got))
		}
	}
	// 新取出的默认值必须和包级默认一致，且改它不会串到下一份
	d := GetDefaultDanmakuConfig()
	if d.Opacity == nil || *d.Opacity != 255 || d.Outline == nil || *d.Outline != 1 {
		t.Fatalf("%s: GetDefaultDanmakuConfig 返回 %v/%v, 期望 255/1", after, derefInt(d.Opacity), derefInt(d.Outline))
	}
	*d.Outline = 7
	if *defaultDanmakuConfig.Outline != 1 {
		t.Fatalf("%s: 修改 GetDefaultDanmakuConfig 的返回值写回了包级默认值", after)
	}
}

func derefInt(p *int) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func derefBool(p *bool) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestCustomDanmakuYamlDoesNotPollutePackageDefaults(t *testing.T) {
	yamlText := `danmaku:
  font_size: 60
  outline: 4
  opacity: 10
  record_gift: false
  record_douyu_gift: false
  record_douyin_gift: false
  record_guard: false
  record_super_chat: false
`
	c, err := NewConfigWithBytes([]byte(yamlText))
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
	if c.Danmaku.Outline == nil || *c.Danmaku.Outline != 4 {
		t.Fatalf("测试配置本身没生效: outline=%v", derefInt(c.Danmaku.Outline))
	}
	assertPackageDanmakuDefaultsIntact(t, "YAML 加载之后")

	// 运行时在配置对象上改值同样不许回写到默认值
	live := NewConfig()
	*live.Danmaku.Outline = 99
	*live.Danmaku.Opacity = 1
	*live.Danmaku.RecordGift = false
	assertPackageDanmakuDefaultsIntact(t, "运行时改值之后")
}

func TestFailedLoadDoesNotPollutePackageDefaults(t *testing.T) {
	if _, err := NewConfigWithBytes([]byte("danmaku:\n  opacity: not_a_number\n")); err == nil {
		t.Fatal("非法 YAML 期望加载失败")
	}
	assertPackageDanmakuDefaultsIntact(t, "加载失败之后")
}

func TestMergeResultDoesNotSharePointers(t *testing.T) {
	base := GetDefaultDanmakuConfig()
	override := GetDefaultDanmakuConfig()
	override.Outline = IntPtr(3)
	merged := mergeDanmakuConfig(&base, &override)
	if merged.Outline == nil {
		t.Fatal("合并结果 outline 为空")
	}
	*merged.Outline = 4
	if *override.Outline != 3 {
		t.Fatalf("改合并结果把 override 改成了 %d, 期望 3", *override.Outline)
	}
	if *base.Outline != 1 {
		t.Fatalf("改合并结果把 base 改成了 %d, 期望 1", *base.Outline)
	}
}

func TestOpacity128MigratesOnlyOnce(t *testing.T) {
	c, err := NewConfigWithBytes([]byte("danmaku:\n  opacity: 128\n"))
	if err != nil {
		t.Fatal(err)
	}
	// 老配置：遗留的默认 128 升到 255，并落下一次性标记
	if got := derefInt(c.Danmaku.Opacity); got != 255 {
		t.Fatalf("首次加载 opacity = %v, 期望 255", got)
	}
	if !c.DanmakuOpacityMigrated {
		t.Fatal("首次加载没有落下迁移标记")
	}

	// 用户在新版面板里重新选 128，存盘再加载必须原样保留
	c.Danmaku.Opacity = IntPtr(128)
	room := LiveRoom{Url: "https://live.bilibili.com/1", OverridableConfig: OverridableConfig{Danmaku: &DanmakuConfig{}}}
	room.Danmaku.Opacity = IntPtr(128)
	c.LiveRooms = append(c.LiveRooms, room)
	pc := PlatformConfig{OverridableConfig: OverridableConfig{Danmaku: &DanmakuConfig{}}}
	pc.Danmaku.Opacity = IntPtr(128)
	c.PlatformConfigs["bilibili"] = pc

	b, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := NewConfigWithBytes(b)
	if err != nil {
		t.Fatalf("带迁移标记的配置二次加载失败: %v", err)
	}
	if got := derefInt(c2.Danmaku.Opacity); got != 128 {
		t.Fatalf("往返后全局 opacity = %v, 期望保持 128", got)
	}
	if got := derefInt(c2.LiveRooms[0].Danmaku.Opacity); got != 128 {
		t.Fatalf("往返后房间级 opacity = %v, 期望保持 128", got)
	}
	if got := derefInt(c2.PlatformConfigs["bilibili"].Danmaku.Opacity); got != 128 {
		t.Fatalf("往返后平台级 opacity = %v, 期望保持 128", got)
	}
}
