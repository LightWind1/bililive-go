package danmaku

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/recorders/danmaku/douyu"
)

// DouyuDanmakuRecorder 斗鱼弹幕录制器
type DouyuDanmakuRecorder struct {
	baseRecorder
	roomID  string
	cookies string
	client  *douyu.DouyuClient
}

// NewDouyuDanmakuRecorder 创建斗鱼弹幕录制器
func NewDouyuDanmakuRecorder(roomID, cookies, outputFile string, cfg configs.DanmakuConfig, logger *logrus.Entry) *DouyuDanmakuRecorder {
	return &DouyuDanmakuRecorder{
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
func (r *DouyuDanmakuRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return nil
	}

	startAt := time.Now()

	assWriter, err := NewAssWriter(r.outputFile, startAt, r.cfg, "Douyu Danmaku")
	if err != nil {
		return err
	}
	r.assWriter = assWriter
	r.startAt = startAt

	var onGift func(username, giftName string, num int)
	if r.cfg.RecordDouyuGift != nil && *r.cfg.RecordDouyuGift {
		onGift = func(username, giftName string, num int) {
			r.addGift(time.Now(), username, giftName, num, 0, "")
		}
	}

	r.client = douyu.NewDouyuClient(r.roomID, r.cookies, r.onDanmaku, onGift, r.logger)

	if err := r.client.Start(ctx); err != nil {
		assWriter.Close()
		return err
	}

	r.running = true
	r.logger.Info("斗鱼弹幕录制已启动")

	return nil
}

// Stop 停止弹幕录制
func (r *DouyuDanmakuRecorder) Stop() {
	w := r.stopBase()
	c := r.client
	r.client = nil
	if c != nil {
		c.Stop()
	}
	if w != nil {
		w.Close()
	}
	r.logger.Infof("斗鱼弹幕录制已停止，共录制 %d 条弹幕", r.GetCount())
}

// onDanmaku 弹幕回调
func (r *DouyuDanmakuRecorder) onDanmaku(username, content string, color int) {
	r.addDanmaku(time.Now(), username, content, color)
}
