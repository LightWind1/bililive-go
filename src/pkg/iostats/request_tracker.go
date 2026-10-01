package iostats

import (
	"context"
	"sync"
	"time"

	bilisentry "github.com/bililive-go/bililive-go/src/pkg/sentry"
	"github.com/sirupsen/logrus"
)

const (
	// requestStatusBufferSize 待落库的请求状态缓冲区大小。
	// 按几百个直播间、每轮各产生一条记录估算，一次批量写入之间足够容纳数轮。
	requestStatusBufferSize = 4096
	// requestStatusFlushInterval 批量落库的最长等待时间
	requestStatusFlushInterval = 2 * time.Second
	// requestStatusFlushBatch 单次批量落库的最大条数
	requestStatusFlushBatch = 256
)

// RequestTracker 请求状态追踪器
// 用于记录直播间请求的成功/失败状态
//
// 记录动作发生在直播间轮询的关键路径上，因此不能同步写库：
// 原先每条记录都在一把全局锁下同步执行一次 SQLite 事务，
// 直播间数量多、平台又在报错时，轮询会被磁盘 I/O 串行拖住。
// 这里改为投递到缓冲区，由后台 goroutine 批量落库。
type RequestTracker struct {
	store Store

	queue    chan *RequestStatus
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}

	// acceptMu 保证 Stop 标记停止与 record 投递原子化，避免后台退出后仍有数据进入队列。
	acceptMu sync.RWMutex
	stopped  bool

	// droppedCount 缓冲区满时丢弃的记录数，仅用于日志提示
	droppedMu    sync.Mutex
	droppedCount int
}

// NewRequestTracker 创建请求追踪器并启动后台落库 goroutine
func NewRequestTracker(store Store) *RequestTracker {
	t := &RequestTracker{
		store:  store,
		queue:  make(chan *RequestStatus, requestStatusBufferSize),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	bilisentry.Go(t.run)
	return t
}

// RecordSuccess 记录成功的请求
func (t *RequestTracker) RecordSuccess(liveID, platform string) {
	t.record(liveID, platform, true, "")
}

// RecordFailure 记录失败的请求
func (t *RequestTracker) RecordFailure(liveID, platform string, errMsg string) {
	t.record(liveID, platform, false, errMsg)
}

// record 内部记录方法，不会阻塞调用方
func (t *RequestTracker) record(liveID, platform string, success bool, errMsg string) {
	t.acceptMu.RLock()
	defer t.acceptMu.RUnlock()
	if t.stopped {
		return
	}

	status := &RequestStatus{
		Timestamp:    time.Now().UnixMilli(),
		LiveID:       liveID,
		Platform:     platform,
		Success:      success,
		ErrorMessage: errMsg,
	}

	select {
	case t.queue <- status:
	default:
		// 缓冲区已满，丢弃这条统计数据。
		// 统计数据的完整性远不如录制本身重要，绝不能因此阻塞轮询。
		t.droppedMu.Lock()
		t.droppedCount++
		t.droppedMu.Unlock()
	}
}

// Stop 停止后台落库 goroutine，并把缓冲区中剩余的数据写完
func (t *RequestTracker) Stop() {
	t.stopOnce.Do(func() {
		t.acceptMu.Lock()
		t.stopped = true
		close(t.stopCh)
		t.acceptMu.Unlock()
		<-t.doneCh
	})
}

// run 后台批量落库循环
func (t *RequestTracker) run() {
	defer close(t.doneCh)

	ticker := time.NewTicker(requestStatusFlushInterval)
	defer ticker.Stop()

	batch := make([]*RequestStatus, 0, requestStatusFlushBatch)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := t.store.SaveRequestStatuses(ctx, batch); err != nil {
			logrus.WithError(err).WithField("count", len(batch)).Error("保存请求状态失败")
		}
		cancel()
		batch = batch[:0]
	}

	for {
		select {
		case status := <-t.queue:
			batch = append(batch, status)
			if len(batch) >= requestStatusFlushBatch {
				flush()
			}
		case <-ticker.C:
			flush()
			t.reportDropped()
		case <-t.stopCh:
			// 退出前把已经排队的数据尽量写完
			for {
				select {
				case status := <-t.queue:
					batch = append(batch, status)
					if len(batch) >= requestStatusFlushBatch {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			t.reportDropped()
			return
		}
	}
}

// reportDropped 汇总输出被丢弃的统计数据条数
func (t *RequestTracker) reportDropped() {
	t.droppedMu.Lock()
	dropped := t.droppedCount
	t.droppedCount = 0
	t.droppedMu.Unlock()

	if dropped > 0 {
		logrus.WithField("count", dropped).Warn("请求状态统计写入积压，已丢弃部分记录")
	}
}

// 全局请求追踪器实例
var (
	globalTracker   *RequestTracker
	globalTrackerMu sync.RWMutex
)

// SetGlobalTracker 设置全局请求追踪器
func SetGlobalTracker(tracker *RequestTracker) {
	globalTrackerMu.Lock()
	defer globalTrackerMu.Unlock()
	globalTracker = tracker
}

// GetGlobalTracker 获取全局请求追踪器
func GetGlobalTracker() *RequestTracker {
	globalTrackerMu.RLock()
	defer globalTrackerMu.RUnlock()
	return globalTracker
}

// TrackRequestSuccess 便捷方法：记录成功的请求
func TrackRequestSuccess(liveID, platform string) {
	if tracker := GetGlobalTracker(); tracker != nil {
		tracker.RecordSuccess(liveID, platform)
	}
}

// TrackRequestFailure 便捷方法：记录失败的请求
func TrackRequestFailure(liveID, platform string, errMsg string) {
	if tracker := GetGlobalTracker(); tracker != nil {
		tracker.RecordFailure(liveID, platform, errMsg)
	}
}
