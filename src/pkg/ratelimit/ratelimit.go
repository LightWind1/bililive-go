// Package ratelimit 为每个直播平台提供访问频率限制功能
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// PlatformRateLimiter 管理各个直播平台的访问频率限制
type PlatformRateLimiter struct {
	limiters map[string]*PlatformLimiter // 平台名称 -> 限制器
	mu       sync.RWMutex                // 读写锁保护map
	enabled  bool                        // 是否实际应用平台级访问限制
}

// PlatformLimiter 单个平台的频率限制器
type PlatformLimiter struct {
	minInterval time.Duration // 最小访问间隔
	lastAccess  time.Time     // 上次访问时间
	mu          sync.Mutex    // 保护访问时间的互斥锁
	inFlight    chan struct{} // 限制同一平台最多一个请求在途
}

// 平台级访问限制暂时关闭，以避免同平台请求串行排队导致新直播间长时间无法开始录制。
// 保留限制器实现和配置结构，后续改善调度策略后可重新启用。
const globalRateLimitEnabled = false

var globalRateLimiter = newPlatformRateLimiter(globalRateLimitEnabled)

func newPlatformRateLimiter(enabled bool) *PlatformRateLimiter {
	return &PlatformRateLimiter{
		limiters: make(map[string]*PlatformLimiter),
		enabled:  enabled,
	}
}

// GetGlobalRateLimiter 获取全局速率限制器实例
func GetGlobalRateLimiter() *PlatformRateLimiter {
	return globalRateLimiter
}

// Enabled 返回当前限制器是否实际应用平台级访问限制。
func (prl *PlatformRateLimiter) Enabled() bool {
	return prl.enabled
}

// SetPlatformLimit 设置或更新指定平台的访问频率限制
func (prl *PlatformRateLimiter) SetPlatformLimit(platform string, intervalSec int) {
	if !prl.enabled {
		return
	}

	if intervalSec <= 0 {
		// 如果间隔为0或负数，移除该平台的限制
		prl.mu.Lock()
		delete(prl.limiters, platform)
		prl.mu.Unlock()
		return
	}

	interval := time.Duration(intervalSec) * time.Second

	prl.mu.Lock()
	defer prl.mu.Unlock()

	if limiter, exists := prl.limiters[platform]; exists {
		// 更新现有限制器的间隔
		limiter.mu.Lock()
		limiter.minInterval = interval
		limiter.mu.Unlock()
	} else {
		// 创建新的限制器
		prl.limiters[platform] = &PlatformLimiter{
			minInterval: interval,
			lastAccess:  time.Time{}, // 零值时间，首次访问不会被限制
			inFlight:    make(chan struct{}, 1),
		}
	}
}

// EnsurePlatformLimit 在平台尚无限制时设置默认值，不覆盖已经显式配置的间隔。
// 用于房间对象早于配置持久化创建的路径，保证首次请求也不会绕过平台保护。
func (prl *PlatformRateLimiter) EnsurePlatformLimit(platform string, intervalSec int) {
	if !prl.enabled || platform == "" || intervalSec <= 0 {
		return
	}

	prl.mu.Lock()
	defer prl.mu.Unlock()
	if _, exists := prl.limiters[platform]; exists {
		return
	}
	prl.limiters[platform] = &PlatformLimiter{
		minInterval: time.Duration(intervalSec) * time.Second,
		lastAccess:  time.Time{},
		inFlight:    make(chan struct{}, 1),
	}
}

// WaitForPlatform 等待直到允许访问指定平台
// 如果平台没有设置限制，立即返回
// 注意：此函数在等待期间不持有锁，以允许 ForceAccess 等操作可以随时执行
func (prl *PlatformRateLimiter) WaitForPlatform(platform string) {
	prl.WaitForPlatformWithContext(context.Background(), platform)
}

// WaitForPlatformWithContext 等待直到允许访问指定平台，支持 context 取消
// 如果平台没有设置限制，立即返回 true
// 返回 true 表示成功获取访问权限，false 表示被 context 取消
func (prl *PlatformRateLimiter) WaitForPlatformWithContext(ctx context.Context, platform string) bool {
	if ctx.Err() != nil {
		return false
	}

	if !prl.enabled {
		return true
	}

	prl.mu.RLock()
	limiter, exists := prl.limiters[platform]
	prl.mu.RUnlock()

	if !exists {
		// 平台没有设置限制，立即返回
		return true
	}

	return waitForLimiterWithContext(ctx, limiter)
}

// AcquirePlatformWithContext 获取指定平台的请求许可，并保证同一平台最多只有一个请求在途。
// 返回的 release 必须在请求完成后调用；平台未配置限制时返回空操作 release。
//
// 仅限制请求开始间隔不足以保护启动阶段：当前一个请求耗时超过最小间隔时，后续请求仍会
// 重叠执行。这里把并发槽位与开始间隔合并为一次许可，使大量直播间并发初始化时仍按平台串行。
func (prl *PlatformRateLimiter) AcquirePlatformWithContext(ctx context.Context, platform string) (release func(), ok bool) {
	if ctx.Err() != nil {
		return nil, false
	}

	if !prl.enabled {
		return func() {}, true
	}

	prl.mu.RLock()
	limiter, exists := prl.limiters[platform]
	prl.mu.RUnlock()
	if !exists {
		return func() {}, true
	}

	select {
	case limiter.inFlight <- struct{}{}:
	case <-ctx.Done():
		return nil, false
	}

	releaseSlot := func() { <-limiter.inFlight }
	if !waitForLimiterWithContext(ctx, limiter) {
		releaseSlot()
		return nil, false
	}

	var once sync.Once
	return func() {
		once.Do(releaseSlot)
	}, true
}

func waitForLimiterWithContext(ctx context.Context, limiter *PlatformLimiter) bool {
	for {
		// 检查 context 是否已取消
		select {
		case <-ctx.Done():
			return false
		default:
		}

		// 获取锁，计算等待时间
		limiter.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(limiter.lastAccess)

		if elapsed >= limiter.minInterval {
			// 已经等待足够长时间，更新访问时间并返回
			limiter.lastAccess = now
			limiter.mu.Unlock()
			return true
		}

		// 计算需要等待的时间
		waitTime := limiter.minInterval - elapsed
		limiter.mu.Unlock() // 释放锁再 sleep，避免阻塞 ForceAccess 等操作

		// 在不持有锁的情况下等待，支持 context 取消
		timer := time.NewTimer(waitTime)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			// 循环回去重新检查
		}
	}
}

// GetPlatformNextAllowedTime 获取平台下次允许访问的时间
func (prl *PlatformRateLimiter) GetPlatformNextAllowedTime(platform string) time.Time {
	if !prl.enabled {
		return time.Now()
	}

	prl.mu.RLock()
	limiter, exists := prl.limiters[platform]
	prl.mu.RUnlock()

	if !exists {
		// 没有限制，立即可访问
		return time.Now()
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	return limiter.lastAccess.Add(limiter.minInterval)
}

// RemovePlatformLimit 移除指定平台的访问限制
func (prl *PlatformRateLimiter) RemovePlatformLimit(platform string) {
	prl.mu.Lock()
	defer prl.mu.Unlock()

	delete(prl.limiters, platform)
}

// GetAllPlatformLimits 获取所有平台的当前限制设置
func (prl *PlatformRateLimiter) GetAllPlatformLimits() map[string]int {
	if !prl.enabled {
		return map[string]int{}
	}

	prl.mu.RLock()
	defer prl.mu.RUnlock()

	limits := make(map[string]int)
	for platform, limiter := range prl.limiters {
		limiter.mu.Lock()
		limits[platform] = int(limiter.minInterval.Seconds())
		limiter.mu.Unlock()
	}

	return limits
}

// WaitInfo 包含平台等待状态信息
type WaitInfo struct {
	WaitedSeconds    float64 // 自上次请求以来已等待的秒数
	NextRequestInSec float64 // 预计多少秒后可以发送下一次请求（0 表示立即可以）
	MinIntervalSec   int     // 平台设置的最小访问间隔
}

// GetPlatformWaitInfo 获取指定平台的等待状态信息
func (prl *PlatformRateLimiter) GetPlatformWaitInfo(platform string) WaitInfo {
	if !prl.enabled {
		return WaitInfo{}
	}

	prl.mu.RLock()
	limiter, exists := prl.limiters[platform]
	prl.mu.RUnlock()

	if !exists {
		// 平台没有设置限制
		return WaitInfo{
			WaitedSeconds:    0,
			NextRequestInSec: 0,
			MinIntervalSec:   0,
		}
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(limiter.lastAccess)

	info := WaitInfo{
		WaitedSeconds:  elapsed.Seconds(),
		MinIntervalSec: int(limiter.minInterval.Seconds()),
	}

	if elapsed < limiter.minInterval {
		// 还需要等待
		info.NextRequestInSec = (limiter.minInterval - elapsed).Seconds()
	}

	return info
}

// ForceAccess 强制访问平台，忽略频率限制
// 返回距离上次访问的时间间隔
func (prl *PlatformRateLimiter) ForceAccess(platform string) time.Duration {
	if !prl.enabled {
		return 0
	}

	prl.mu.RLock()
	limiter, exists := prl.limiters[platform]
	prl.mu.RUnlock()

	if !exists {
		return 0
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(limiter.lastAccess)
	limiter.lastAccess = now
	return elapsed
}
