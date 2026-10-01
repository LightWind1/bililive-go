package danmaku

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

// 本文件守住 issue #1178 的修复：车道分配只改垂直位置，绝不改弹幕的到达时间。
// 旧实现在所有车道被占用时把新弹幕的起点推到"上一条离开屏幕的时刻"，并在新起点上
// 再叠加一个净空时长，占用点随消息条数线性前进且不收敛——录制几分钟能排出几小时的
// 字幕，时间轴与视频彻底对不上。

func newLaneTestWriter(t *testing.T, scrollTime int) *AssWriter {
	t.Helper()
	cfg := configs.GetDefaultDanmakuConfig()
	cfg.ScrollTime = scrollTime
	cfg.ScrollArea = "full"
	cfg.Resolution = "1920x1080"
	cfg.FontSize = 36
	w, err := NewAssWriter(filepath.Join(t.TempDir(), "lane.ass"), time.Now(), cfg, "lane")
	if err != nil {
		t.Fatalf("创建 AssWriter 失败: %v", err)
	}
	return w
}

func parseAssTimeCS(t *testing.T, s string) int64 {
	t.Helper()
	var h, m, sec, cs int64
	if _, err := fmt.Sscanf(s, "%d:%d:%d.%d", &h, &m, &sec, &cs); err != nil {
		t.Fatalf("无法解析 ASS 时间 %q: %v", s, err)
	}
	return ((h*60+m)*60+sec)*100 + cs
}

// TestAssignLaneOccupiesOneClearWindow 验证每条弹幕对车道的占用恰好一个净空窗口。
func TestAssignLaneOccupiesOneClearWindow(t *testing.T) {
	w := newLaneTestWriter(t, 10)
	defer w.Close()

	const startCS int64 = 1000
	textWidth := w.estimateTextWidth("用户: 弹幕内容一二三四五六七八")
	wantTail := startCS + w.travelCS(textWidth+w.cfg.FontSize)

	used := make(map[int]bool, w.laneNum)
	for i := 0; i < w.laneNum; i++ {
		lane := w.assignLane(startCS, textWidth)
		if used[lane] {
			t.Fatalf("第 %d 次分配复用了仍被占用的车道 %d", i, lane)
		}
		used[lane] = true
		if got := w.laneLast[lane]; got != wantTail {
			t.Fatalf("车道 %d 占用点 = %d, 期望 %d", lane, got, wantTail)
		}
	}

	// 车道全满：只能复用最早离开的那条，占用点仍以本条到达时刻为基准。
	// 旧实现在这里写成 到达时刻 + 2 倍净空时长，于是每多一条就多推一截。
	lane := w.assignLane(startCS, textWidth)
	if got := w.laneLast[lane]; got != wantTail {
		t.Fatalf("车道耗尽后占用点 = %d, 期望 %d（不得级联累积）", got, wantTail)
	}
}

// TestAddDanmakuKeepsArrivalTimeline 端到端验证超车道吞吐的突发流量下，
// 写出的时间戳仍等于真实到达时刻，且尾部占用不随条数累积。
func TestAddDanmakuKeepsArrivalTimeline(t *testing.T) {
	const (
		count    = 300
		interval = 50 * time.Millisecond // 20 条/秒，明显高于 27 车道的吞吐
	)
	w := newLaneTestWriter(t, 10)
	path := w.OutputPath()
	start := w.startAt
	const username, text = "用户", "弹幕内容一二三四五六七八"

	for i := 0; i < count; i++ {
		w.AddDanmaku(start.Add(time.Duration(i)*interval), username, text, 16777215)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 AssWriter 失败: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 ASS 失败: %v", err)
	}
	textWidth := w.estimateTextWidth(username + ": " + text)
	durationCS := w.travelCS(w.resX + textWidth)
	maxTailCS := int64(count-1)*int64(interval/(10*time.Millisecond)) + durationCS

	var starts, ends []int64
	lanes := make(map[int]bool)
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "Dialogue: ") {
			continue
		}
		fields := strings.Split(line, ",")
		starts = append(starts, parseAssTimeCS(t, fields[1]))
		ends = append(ends, parseAssTimeCS(t, fields[2]))
		var marginV int
		if _, err := fmt.Sscanf(fields[7], "%d", &marginV); err != nil {
			t.Fatalf("无法解析 MarginV %q: %v", fields[7], err)
		}
		lanes[marginV/(w.cfg.FontSize+4)] = true
	}

	if len(starts) != count {
		t.Fatalf("写出 %d 条弹幕, 期望 %d 条", len(starts), count)
	}
	if len(lanes) != w.laneNum {
		t.Fatalf("只用到 %d 条车道, 期望 %d 条（车道未被轮转）", len(lanes), w.laneNum)
	}
	for i := range starts {
		if want := int64(i) * int64(interval/(10*time.Millisecond)); starts[i] != want {
			t.Fatalf("第 %d 条起点 = %d 厘秒, 期望 %d 厘秒（到达时刻被改写）", i, starts[i], want)
		}
		if got := ends[i] - starts[i]; got != durationCS {
			t.Fatalf("第 %d 条时长 = %d 厘秒, 期望 %d 厘秒（与 Banner 速度口径不一致）", i, got, durationCS)
		}
		if ends[i] > maxTailCS {
			t.Fatalf("第 %d 条结束于 %d 厘秒, 超过上界 %d 厘秒（车道占用级联累积）", i, ends[i], maxTailCS)
		}
	}
}

// TestAssignLaneKeepsEarlierReservation 验证短弹幕不会把同车道长弹幕已预留的占用点提前。
// 占用点一旦被短弹幕抹掉，这条车道就永远是最早空闲的那条，后续弹幕全部集中叠印在同一行。
func TestAssignLaneKeepsEarlierReservation(t *testing.T) {
	w := newLaneTestWriter(t, 10)
	defer w.Close()

	longWidth := w.estimateTextWidth("用户昵称比较长: 这是一条相当长的弹幕内容用来占满一整屏宽度")
	for i := 0; i < w.laneNum; i++ {
		w.assignLane(0, longWidth)
	}

	lane := w.assignLane(100, w.estimateTextWidth("好"))
	if got, want := w.laneLast[lane], w.travelCS(longWidth+w.cfg.FontSize); got != want {
		t.Fatalf("车道 %d 占用点 = %d 厘秒, 期望保持长弹幕的 %d 厘秒（短弹幕把长弹幕的预留抹掉了）", lane, got, want)
	}
}

// TestAssignLaneSpreadsSaturatedBurst 验证车道耗尽后的突发在各行间轮转摊开，
// 而不是集中堆叠到一条车道上。
func TestAssignLaneSpreadsSaturatedBurst(t *testing.T) {
	w := newLaneTestWriter(t, 10)
	defer w.Close()

	longWidth := w.estimateTextWidth("用户昵称比较长: 这是一条相当长的弹幕内容用来占满一整屏宽度")
	shortWidth := w.estimateTextWidth("好")
	for i := 0; i < w.laneNum; i++ {
		w.assignLane(0, longWidth)
	}

	const burst = 54
	counts := make(map[int]int, w.laneNum)
	for i := 0; i < burst; i++ {
		counts[w.assignLane(int64(100+i), shortWidth)]++
	}
	if len(counts) != w.laneNum {
		t.Fatalf("突发只用了 %d 条车道, 期望全部 %d 条", len(counts), w.laneNum)
	}
	limit := burst/w.laneNum + 1
	if got := maxLaneCount(counts); got > limit {
		t.Fatalf("单车道最多 %d 条, 超过均摊上界 %d 条（堆叠在同一行）", got, limit)
	}
}

func maxLaneCount(counts map[int]int) int {
	max := 0
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	return max
}
