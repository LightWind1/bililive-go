package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestPlatformRateLimiter(t *testing.T) {
	limiter := newPlatformRateLimiter(true)

	// 设置测试平台限制：2秒间隔
	limiter.SetPlatformLimit("test_platform", 2)

	// 第一次访问应该立即通过
	start := time.Now()
	limiter.WaitForPlatform("test_platform")
	elapsed1 := time.Since(start)

	if elapsed1 > 100*time.Millisecond {
		t.Errorf("First access should be immediate, took %v", elapsed1)
	}

	// 第二次访问应该等待约2秒
	start = time.Now()
	limiter.WaitForPlatform("test_platform")
	elapsed2 := time.Since(start)

	if elapsed2 < 1900*time.Millisecond || elapsed2 > 2100*time.Millisecond {
		t.Errorf("Second access should wait ~2s, took %v", elapsed2)
	}

	// 测试没有限制的平台应该立即通过
	start = time.Now()
	limiter.WaitForPlatform("unlimited_platform")
	elapsed3 := time.Since(start)

	if elapsed3 > 100*time.Millisecond {
		t.Errorf("Unlimited platform access should be immediate, took %v", elapsed3)
	}

	// 清理
	limiter.RemovePlatformLimit("test_platform")
}

func TestPlatformRateLimiterUpdate(t *testing.T) {
	limiter := newPlatformRateLimiter(true)

	// 设置初始限制
	limiter.SetPlatformLimit("update_test", 3)

	// 更新限制
	limiter.SetPlatformLimit("update_test", 1)

	// 第一次访问应该立即通过
	limiter.WaitForPlatform("update_test")

	// 验证新的限制生效
	start := time.Now()
	limiter.WaitForPlatform("update_test")
	elapsed := time.Since(start)

	if elapsed < 900*time.Millisecond || elapsed > 1100*time.Millisecond {
		t.Errorf("Updated limit should wait ~1s, took %v", elapsed)
	}

	// 清理
	limiter.RemovePlatformLimit("update_test")
}

func TestConfigSyncRateLimits(t *testing.T) {
	// 这个测试需要配置系统的支持，暂时跳过具体实现
	t.Skip("Config sync test requires full config system")
}

func TestAcquirePlatformSerializesInFlightRequests(t *testing.T) {
	limiter := &PlatformRateLimiter{
		limiters: map[string]*PlatformLimiter{
			"test": {
				inFlight: make(chan struct{}, 1),
			},
		},
		enabled: true,
	}

	releaseFirst, ok := limiter.AcquirePlatformWithContext(context.Background(), "test")
	if !ok {
		t.Fatal("首次请求未获取到平台许可")
	}

	secondAcquired := make(chan func(), 1)
	go func() {
		release, acquired := limiter.AcquirePlatformWithContext(context.Background(), "test")
		if acquired {
			secondAcquired <- release
		}
	}()

	select {
	case release := <-secondAcquired:
		release()
		t.Fatal("首个请求未释放时，第二个同平台请求不应进入")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()
	select {
	case release := <-secondAcquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("首个请求释放后，第二个请求未获取平台许可")
	}
}

func TestEnsurePlatformLimitDoesNotOverrideExplicitLimit(t *testing.T) {
	limiter := newPlatformRateLimiter(true)
	limiter.SetPlatformLimit("test", 5)
	limiter.EnsurePlatformLimit("test", 1)

	if got := limiter.GetAllPlatformLimits()["test"]; got != 5 {
		t.Fatalf("兜底限制覆盖了显式配置：得到 %d 秒，期望 5 秒", got)
	}
}

func TestGlobalPlatformRateLimiterDisabled(t *testing.T) {
	limiter := GetGlobalRateLimiter()
	limiter.SetPlatformLimit("test", 60)
	if limiter.Enabled() {
		t.Fatal("全局平台限制器应保持关闭")
	}

	if limits := limiter.GetAllPlatformLimits(); len(limits) != 0 {
		t.Fatalf("全局平台限制器应保持关闭，实际限制：%v", limits)
	}

	release, ok := limiter.AcquirePlatformWithContext(context.Background(), "test")
	if !ok {
		t.Fatal("关闭平台限制后请求应立即获准")
	}
	release()

	waitInfo := limiter.GetPlatformWaitInfo("test")
	if waitInfo.MinIntervalSec != 0 || waitInfo.NextRequestInSec != 0 {
		t.Fatalf("关闭平台限制后不应报告等待状态：%+v", waitInfo)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if limiter.WaitForPlatformWithContext(canceledCtx, "test") {
		t.Fatal("关闭平台限制后仍应尊重 context 取消")
	}
	if release, acquired := limiter.AcquirePlatformWithContext(canceledCtx, "test"); acquired || release != nil {
		t.Fatal("context 已取消时不应获取平台许可")
	}
}
