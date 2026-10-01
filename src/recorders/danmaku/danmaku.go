package danmaku

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/recorders/danmaku/bilibili"
)

// DanmakuRecorder 哔哩哔哩弹幕录制器
type DanmakuRecorder struct {
	baseRecorder
	roomID  int
	cookies string
	client  *bilibili.Client
}

// NewDanmakuRecorder 创建哔哩哔哩弹幕录制器
func NewDanmakuRecorder(roomID int, cookies string, outputFile string, cfg configs.DanmakuConfig, logger *logrus.Entry) *DanmakuRecorder {
	return &DanmakuRecorder{
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
func (d *DanmakuRecorder) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return nil
	}

	d.startAt = time.Now()

	assWriter, err := NewAssWriter(d.outputFile, d.startAt, d.cfg, "Bilibili Danmaku")
	if err != nil {
		return fmt.Errorf("failed to create ass writer: %w", err)
	}
	d.assWriter = assWriter

	c := bilibili.NewClient(d.roomID, d.cookies, d.logger)

	c.OnDanmaku(func(msg bilibili.DanmakuMsg) {
		d.addDanmaku(time.Now(), msg.Uname, msg.Content, msg.Color)
	})

	if d.cfg.RecordGift != nil && *d.cfg.RecordGift {
		c.OnGift(func(msg bilibili.GiftMsg) {
			if msg.Num > 0 {
				d.addGift(time.Now(), msg.Uname, msg.GiftName, msg.Num, msg.Price, msg.CoinType)
			}
		})
	}

	if d.cfg.RecordGuard != nil && *d.cfg.RecordGuard {
		c.OnGuardBuy(func(msg bilibili.GuardBuyMsg) {
			d.addGuard(time.Now(), msg.Username, msg.GiftName, msg.Price)
		})
	}

	if d.cfg.RecordSuperChat != nil && *d.cfg.RecordSuperChat {
		c.OnSuperChat(func(msg bilibili.SuperChatMsg) {
			d.addSuperChat(time.Now(), msg.Uname, msg.Message, msg.Price)
		})
	}

	if err := c.Start(); err != nil {
		assWriter.Close()
		d.assWriter = nil
		return fmt.Errorf("failed to start bilibili danmaku client: %w", err)
	}

	d.client = c
	d.running = true
	d.logger.Info("弹幕录制已启动")

	go func() {
		<-ctx.Done()
		d.Stop()
	}()

	return nil
}

// Stop 停止弹幕录制
func (d *DanmakuRecorder) Stop() {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	d.running = false
	w := d.assWriter
	d.assWriter = nil
	c := d.client
	d.client = nil
	d.mu.Unlock()

	if c != nil {
		c.Stop()
	}
	if w != nil {
		w.Close()
	}
	count := d.GetCount()
	if count > 0 {
		d.logger.Infof("弹幕录制已停止，共录制 %d 条弹幕 -> %s", count, d.outputFile)
	} else {
		d.logger.Info("弹幕录制已停止，未收到弹幕")
	}
}
