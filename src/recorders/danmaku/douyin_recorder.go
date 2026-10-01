package danmaku

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/recorders/danmaku/douyin"
)

// DouyinDanmakuRecorder 抖音弹幕录制器
type DouyinDanmakuRecorder struct {
	baseRecorder
	roomID  string
	cookies string
	client  *douyin.DouyinClient
}

// NewDouyinDanmakuRecorder 创建抖音弹幕录制器
func NewDouyinDanmakuRecorder(roomID, cookies, outputFile string, cfg configs.DanmakuConfig, logger *logrus.Entry) *DouyinDanmakuRecorder {
	return &DouyinDanmakuRecorder{
		baseRecorder: baseRecorder{
			outputFile: outputFile,
			cfg:        cfg,
			logger:     logger,
		},
		roomID:  roomID,
		cookies: cookies,
	}
}

// Start 开始弹幕录制
func (r *DouyinDanmakuRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return nil
	}

	r.startAt = time.Now()

	assWriter, err := NewAssWriter(r.outputFile, r.startAt, r.cfg, "Douyin Danmaku")
	if err != nil {
		return err
	}
	r.assWriter = assWriter

	var onGift func(username, giftName string, num int)
	if r.cfg.RecordDouyinGift != nil && *r.cfg.RecordDouyinGift {
		onGift = func(username, giftName string, num int) {
			r.addGift(time.Now(), username, giftName, num, 0, "")
		}
	}
	r.client = douyin.NewDouyinClient(r.roomID, r.cookies, r.onDanmaku, onGift, r.logger)

	if err := r.client.Start(ctx); err != nil {
		assWriter.Close()
		return err
	}

	r.running = true
	r.logger.Info("抖音弹幕录制已启动")

	return nil
}

// Stop 停止弹幕录制
func (r *DouyinDanmakuRecorder) Stop() {
	w := r.stopBase()
	c := r.client
	r.client = nil
	if c != nil {
		c.Stop()
	}
	if w != nil {
		w.Close()
	}
	r.logger.Infof("抖音弹幕录制已停止，共录制 %d 条弹幕", r.GetCount())
}

// onDanmaku 弹幕回调
func (r *DouyinDanmakuRecorder) onDanmaku(username, content string) {
	r.addDanmaku(time.Now(), username, content, 16777215)
}
