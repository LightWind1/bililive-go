package iostats

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeStore 只实现请求状态的批量写入，其余方法继承自嵌入的接口（测试中不会调用）
type fakeStore struct {
	Store

	mu       sync.Mutex
	saved    []*RequestStatus
	batches  int
	blockFor time.Duration
}

func (f *fakeStore) SaveRequestStatuses(ctx context.Context, statuses []*RequestStatus) error {
	if f.blockFor > 0 {
		time.Sleep(f.blockFor)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, statuses...)
	f.batches++
	return nil
}

func (f *fakeStore) snapshot() (count, batches int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved), f.batches
}

// Stop 必须把缓冲区里尚未落库的记录写完
func TestRequestTrackerFlushesOnStop(t *testing.T) {
	store := &fakeStore{}
	tracker := NewRequestTracker(store)

	const total = 50
	for i := 0; i < total; i++ {
		tracker.RecordFailure("live-1", "抖音", "请求失败: 500 Internal Server Error")
	}
	tracker.Stop()

	count, batches := store.snapshot()
	if count != total {
		t.Fatalf("落库记录数 = %d，期望 %d", count, total)
	}
	if batches == 0 {
		t.Fatal("没有发生任何批量写入")
	}
	if batches > total {
		t.Fatalf("批量写入次数 = %d，没有起到合批效果", batches)
	}
}

// 记录动作在轮询关键路径上，即使存储很慢也不能阻塞调用方
func TestRequestTrackerRecordDoesNotBlock(t *testing.T) {
	store := &fakeStore{blockFor: 50 * time.Millisecond}
	tracker := NewRequestTracker(store)
	defer tracker.Stop()

	// 远超缓冲区容量，超出的部分应被丢弃而不是阻塞
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < requestStatusBufferSize*2; i++ {
			tracker.RecordSuccess("live-1", "抖音")
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("记录请求状态阻塞了调用方")
	}
}

func TestRequestTrackerIgnoresRecordsAfterStop(t *testing.T) {
	store := &fakeStore{}
	tracker := NewRequestTracker(store)
	tracker.RecordSuccess("before-stop", "抖音")
	tracker.Stop()

	before, _ := store.snapshot()
	tracker.RecordFailure("after-stop", "抖音", "不应入队")
	if queued := len(tracker.queue); queued != 0 {
		t.Fatalf("停止后仍有 %d 条记录进入无人消费的队列", queued)
	}
	after, _ := store.snapshot()
	if after != before {
		t.Fatalf("停止后的记录被写入：停止前 %d 条，停止后 %d 条", before, after)
	}
}
