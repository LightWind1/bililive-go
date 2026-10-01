package notify

import (
	"fmt"
	"strings"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/consts"
	"github.com/bililive-go/bililive-go/src/notify/bark"
	"github.com/bililive-go/bililive-go/src/notify/email"
	"github.com/bililive-go/bililive-go/src/notify/ntfy"
	"github.com/bililive-go/bililive-go/src/notify/telegram"
	"github.com/bililive-go/bililive-go/src/notify/wxpusher"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
)

// RecordingFileDetail 录制文件详情
type RecordingFileDetail struct {
	Name     string // 文件名（不含路径）
	Size     int64  // 文件大小（字节）
	Uploaded bool   // 是否已上传到云端（用于摘要消息标注）
}

// SendNotification 发送统一通知函数
// 检测用户是否开启了telegram和email通知服务，然后分别发送通知
// 参数: logger(LiveLogger), hostName(主播姓名), platform(直播平台), liveURL(直播地址), status(直播状态: consts.LiveStatusStart/consts.LiveStatusStop), notifyOnly(是否为仅提醒模式)
func SendNotification(logger *livelogger.LiveLogger, hostName, platform, liveURL, status string, notifyOnly ...bool) error {
	// 获取当前配置
	cfg := configs.GetCurrentConfig()
	if cfg == nil {
		return fmt.Errorf("configuration is nil")
	}

	// 判断是否为仅提醒模式
	isNotifyOnly := false
	if len(notifyOnly) > 0 {
		isNotifyOnly = notifyOnly[0]
	}

	// 根据状态和模式设置消息内容
	var messageStatus string
	switch status {
	case consts.LiveStatusStart:
		if isNotifyOnly {
			messageStatus = "已开播，请手动开始录制"
		} else {
			messageStatus = "已开始直播,正在录制中"
		}
	case consts.LiveStatusStop:
		if isNotifyOnly {
			messageStatus = "已结束直播"
		} else {
			messageStatus = "已结束直播,录制已停止"
		}
	default:
		messageStatus = "直播状态未知"
	}

	// 统一主播信息格式
	hostInfo := fmt.Sprintf("%s,%s", hostName, messageStatus)

	// 构造Telegram消息内容 (包含所有信息)
	telegramMessage := fmt.Sprintf("主播：%s\n平台：%s\n直播地址：%s", hostInfo, platform, liveURL)

	// 检查是否开启了Telegram通知服务
	if cfg.Notify.Telegram.Enable {
		// 发送Telegram通知
		err := telegram.SendMessage(
			cfg.Notify.Telegram.BotToken,
			cfg.Notify.Telegram.ChatID,
			telegramMessage,
			cfg.Notify.Telegram.WithNotification, // 发送带提醒的消息
		)
		if err != nil {
			logger.WithError(err).Error("Failed to send Telegram message")
			// 注意：即使Telegram发送失败，我们仍然继续尝试发送邮件
		}
	}

	// 构造邮件主题和内容
	emailSubject := fmt.Sprintf("%s - %s", hostInfo, platform)
	emailBody := fmt.Sprintf("主播：%s\n平台：%s\n直播地址：%s", hostInfo, platform, liveURL)

	// 检查是否开启了Email通知服务
	if cfg.Notify.Email.Enable {
		// 发送Email通知
		err := email.SendEmail(emailSubject, emailBody)
		if err != nil {
			logger.WithError(err).Error("Failed to send email")
		}
	}

	// 检查是否开启了Ntfy通知服务
	if cfg.Notify.Ntfy.Enable {
		// 根据不同的状态发送不同的ntfy消息
		var err error
		switch status {
		case consts.LiveStatusStart:
			// 从配置中获取scheme URL
			var schemeUrl string
			// 根据liveURL查找对应的LiveRoom配置
			if liveRoom, lookupErr := cfg.GetLiveRoomByUrl(liveURL); lookupErr == nil {
				schemeUrl = liveRoom.SchemeUrl
			}

			// 发送Ntfy开始录制通知
			err = ntfy.SendMessage(
				cfg.Notify.Ntfy.URL,
				cfg.Notify.Ntfy.Token,
				cfg.Notify.Ntfy.Tag,
				hostName,
				platform,
				liveURL,
				schemeUrl,
				isNotifyOnly,
			)
		case consts.LiveStatusStop:
			// 发送Ntfy停止录制通知
			err = ntfy.SendStopMessage(
				cfg.Notify.Ntfy.URL,
				cfg.Notify.Ntfy.Token,
				cfg.Notify.Ntfy.Tag,
				hostName,
				platform,
				liveURL,
				isNotifyOnly,
			)
		}

		if err != nil {
			logger.WithError(err).Error("Failed to send Ntfy message")
		}
	}

	// 检查是否开启了 Bark 通知服务
	if cfg.Notify.Bark.Enable {
		var err error
		switch status {
		case consts.LiveStatusStart:
			err = bark.SendMessage(
				cfg.Notify.Bark.ServerURL,
				cfg.Notify.Bark.DeviceKey,
				cfg.Notify.Bark.Sound,
				cfg.Notify.Bark.Group,
				cfg.Notify.Bark.Icon,
				cfg.Notify.Bark.Level,
				hostName,
				platform,
				liveURL,
				isNotifyOnly,
			)
		case consts.LiveStatusStop:
			err = bark.SendStopMessage(
				cfg.Notify.Bark.ServerURL,
				cfg.Notify.Bark.DeviceKey,
				cfg.Notify.Bark.Sound,
				cfg.Notify.Bark.Group,
				cfg.Notify.Bark.Icon,
				cfg.Notify.Bark.Level,
				hostName,
				platform,
				liveURL,
				isNotifyOnly,
			)
		}
		if err != nil {
			logger.WithError(err).Error("Failed to send Bark message")
		}
	}

	// WxPusher 通知
	if cfg.Notify.WxPusher.Enable {
		title := fmt.Sprintf("%s - %s", hostInfo, platform)
		body := fmt.Sprintf("主播：%s\n平台：%s\n直播地址：%s", hostInfo, platform, liveURL)
		if err := wxpusher.SendMessage(
			cfg.Notify.WxPusher.AppToken,
			cfg.Notify.WxPusher.UIDs,
			title,
			body,
		); err != nil {
			logger.WithError(err).Error("Failed to send WxPusher message")
		}
	}

	return nil
}

// SendTestNotification 发送测试通知
func SendTestNotification(logger *livelogger.LiveLogger) {
	// 测试开始直播通知
	err := SendNotification(logger, "测试主播", "测试平台", "https://example.com/live", consts.LiveStatusStart)
	if err != nil {
		logger.WithError(err).Error("Failed to send start live test notification")
	}

	// 测试结束直播通知
	err = SendNotification(logger, "测试主播", "测试平台", "https://example.com/live", consts.LiveStatusStop)
	if err != nil {
		logger.WithError(err).Error("Failed to send stop live test notification")
	}
}

// formatFileSize 将字节数格式化为可读的文件大小
func formatFileSize(size int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case size >= gb:
		return fmt.Sprintf("%.2f GB", float64(size)/float64(gb))
	case size >= mb:
		return fmt.Sprintf("%.2f MB", float64(size)/float64(mb))
	case size >= kb:
		return fmt.Sprintf("%.2f KB", float64(size)/float64(kb))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

// buildRecordingSummaryMessage 构造录制摘要消息内容
// 当文件数量过多时，截断文件列表以避免超出消息平台字符限制（如 Telegram 4096 字符）
// outputPath 用于获取剩余磁盘空间，为空则不显示
func buildRecordingSummaryMessage(hostName, platform string, files []RecordingFileDetail, outputPath string) (title, body string) {
	const maxDisplayFiles = 30 // 最多显示的文件数量

	title = fmt.Sprintf("%s 录制完成", hostName)

	var sb strings.Builder
	fmt.Fprintf(&sb, "平台：%s\n", platform)
	fmt.Fprintf(&sb, "录制文件：%d 个\n", len(files))
	var totalSize int64
	for i, f := range files {
		totalSize += f.Size
		if i < maxDisplayFiles {
			fmt.Fprintf(&sb, "  %d. %s (%s)\n", i+1, f.Name, formatFileSize(f.Size))
		}
	}
	if len(files) > maxDisplayFiles {
		fmt.Fprintf(&sb, "  ... 还有 %d 个文件未显示\n", len(files)-maxDisplayFiles)
	}
	fmt.Fprintf(&sb, "总大小：%s", formatFileSize(totalSize))
	// 显示剩余磁盘空间
	if outputPath != "" {
		if free, err := getDiskFreeSpace(outputPath); err == nil {
			fmt.Fprintf(&sb, "\n剩余磁盘空间：%s", formatFileSize(int64(free)))
		}
	}
	body = sb.String()
	return
}

// buildUploadedSummaryBody 构造"已上传到云端"场景的录制摘要消息体
// 与 buildRecordingSummaryMessage 类似，但标注文件已上传、不显示磁盘空间
func buildUploadedSummaryBody(platform string, files []RecordingFileDetail) string {
	const maxDisplayFiles = 30

	var sb strings.Builder
	fmt.Fprintf(&sb, "平台：%s\n", platform)
	fmt.Fprintf(&sb, "录制文件：%d 个（已上传到云端）\n", len(files))
	var totalSize int64
	for i, f := range files {
		totalSize += f.Size
		if i < maxDisplayFiles {
			fmt.Fprintf(&sb, "  %d. %s (%s)\n", i+1, f.Name, formatFileSize(f.Size))
		}
	}
	if len(files) > maxDisplayFiles {
		fmt.Fprintf(&sb, "  ... 还有 %d 个文件未显示\n", len(files)-maxDisplayFiles)
	}
	fmt.Fprintf(&sb, "总大小：%s\n", formatFileSize(totalSize))
	fmt.Fprintf(&sb, "本地文件已清理")
	return sb.String()
}

// buildMixedSummaryBody 构造"部分上传、部分本地保留"场景的消息体
func buildMixedSummaryBody(platform string, uploaded, kept []RecordingFileDetail, outputPath string) string {
	const maxDisplayFiles = 30

	var sb strings.Builder
	fmt.Fprintf(&sb, "平台：%s\n", platform)

	// 已上传部分
	fmt.Fprintf(&sb, "已上传到云端：%d 个\n", len(uploaded))
	var uploadedSize int64
	for i, f := range uploaded {
		uploadedSize += f.Size
		if i < maxDisplayFiles {
			fmt.Fprintf(&sb, "  %d. %s (%s)\n", i+1, f.Name, formatFileSize(f.Size))
		}
	}
	if len(uploaded) > maxDisplayFiles {
		fmt.Fprintf(&sb, "  ... 还有 %d 个文件未显示\n", len(uploaded)-maxDisplayFiles)
	}
	fmt.Fprintf(&sb, "上传总大小：%s\n", formatFileSize(uploadedSize))

	// 本地保留部分
	fmt.Fprintf(&sb, "本地保留：%d 个\n", len(kept))
	var keptSize int64
	for i, f := range kept {
		keptSize += f.Size
		if i < maxDisplayFiles {
			fmt.Fprintf(&sb, "  %d. %s (%s)\n", i+1, f.Name, formatFileSize(f.Size))
		}
	}
	if len(kept) > maxDisplayFiles {
		fmt.Fprintf(&sb, "  ... 还有 %d 个文件未显示\n", len(kept)-maxDisplayFiles)
	}
	fmt.Fprintf(&sb, "本地总大小：%s", formatFileSize(keptSize))
	if outputPath != "" {
		if free, err := getDiskFreeSpace(outputPath); err == nil {
			fmt.Fprintf(&sb, "\n剩余磁盘空间：%s", formatFileSize(int64(free)))
		}
	}
	return sb.String()
}

// buildRecordingSummaryMessageBody 构造纯本地保留的消息体（不含 title）
func buildRecordingSummaryMessageBody(platform string, files []RecordingFileDetail, outputPath string) string {
	_, body := buildRecordingSummaryMessage("", platform, files, outputPath)
	return body
}

// SendRecordingSummary 录制结束后发送录制文件摘要通知
// outputPath 为录制输出路径，用于获取剩余磁盘空间
func SendRecordingSummary(logger *livelogger.LiveLogger, hostName, platform string, files []RecordingFileDetail, outputPath string) {
	cfg := configs.GetCurrentConfig()
	if cfg == nil || !cfg.Notify.SendRecordingSummary {
		return
	}
	if len(files) == 0 {
		return
	}

	title, body := buildRecordingSummaryMessage(hostName, platform, files, outputPath)
	sendToAllChannels(cfg, logger, title, body)
}

// SendPipelineRecordingSummary Pipeline 完成后发送录制摘要通知
// originalFiles: 原始录制文件列表（allUploaded 时用于显示）
// finalFiles: Pipeline 处理后的最终文件列表（含 Uploaded 标记）
func SendPipelineRecordingSummary(
	logger *livelogger.LiveLogger,
	hostName, platform string,
	originalFiles, finalFiles []RecordingFileDetail,
	outputPath string,
) {
	cfg := configs.GetCurrentConfig()
	if cfg == nil || !cfg.Notify.SendRecordingSummary {
		return
	}

	// 分离已上传文件和本地保留文件
	var uploaded []RecordingFileDetail
	var kept []RecordingFileDetail
	for _, f := range finalFiles {
		if f.Uploaded {
			uploaded = append(uploaded, f)
		} else {
			kept = append(kept, f)
		}
	}

	var title, body string
	if len(uploaded) > 0 && len(kept) == 0 {
		// 所有文件已上传：显示上传文件列表
		title = fmt.Sprintf("%s 录制完成", hostName)
		body = buildUploadedSummaryBody(platform, uploaded)
	} else if len(uploaded) > 0 && len(kept) > 0 {
		// 部分上传、部分保留：显示两段
		title = fmt.Sprintf("%s 录制完成", hostName)
		body = buildMixedSummaryBody(platform, uploaded, kept, outputPath)
	} else if len(kept) > 0 {
		// 全部本地保留
		title, body = buildRecordingSummaryMessage(hostName, platform, kept, outputPath)
	} else if len(originalFiles) > 0 {
		// finalFiles 为空，回退到 originalFiles
		title = fmt.Sprintf("%s 录制完成", hostName)
		body = buildRecordingSummaryMessageBody(platform, originalFiles, outputPath)
	} else {
		return
	}

	sendToAllChannels(cfg, logger, title, body)
}

// SendSystemAlert 发送一条"系统级"告警（非房间开播/下播事件），
// 走用户已配置的全部通知渠道（Telegram/Email/Bark/ntfy/wxpusher）。
// 未配置任何渠道时静默无操作。用于如"斗鱼登录失效需重新扫码"这类全局提醒。
func SendSystemAlert(title, body string) {
	cfg := configs.GetCurrentConfig()
	if cfg == nil {
		return
	}
	sendToAllChannels(cfg, livelogger.New(0, nil), title, body)
}

// sendToAllChannels 向所有已启用的通知通道推送摘要消息
func sendToAllChannels(cfg *configs.Config, logger *livelogger.LiveLogger, title, body string) {
	// Telegram
	if cfg.Notify.Telegram.Enable {
		msg := fmt.Sprintf("%s\n%s", title, body)
		if err := telegram.SendMessage(
			cfg.Notify.Telegram.BotToken,
			cfg.Notify.Telegram.ChatID,
			msg,
			cfg.Notify.Telegram.WithNotification,
		); err != nil {
			logger.WithError(err).Error("Failed to send recording summary via Telegram")
		}
	}

	// Email
	if cfg.Notify.Email.Enable {
		if err := email.SendEmail(title, body); err != nil {
			logger.WithError(err).Error("Failed to send recording summary via Email")
		}
	}

	// Bark
	if cfg.Notify.Bark.Enable {
		if err := bark.SendSummaryMessage(
			cfg.Notify.Bark.ServerURL,
			cfg.Notify.Bark.DeviceKey,
			cfg.Notify.Bark.Sound,
			cfg.Notify.Bark.Group,
			cfg.Notify.Bark.Icon,
			cfg.Notify.Bark.Level,
			title,
			body,
		); err != nil {
			logger.WithError(err).Error("Failed to send recording summary via Bark")
		}
	}

	// WxPusher
	if cfg.Notify.WxPusher.Enable {
		if err := wxpusher.SendMessage(
			cfg.Notify.WxPusher.AppToken,
			cfg.Notify.WxPusher.UIDs,
			title,
			body,
		); err != nil {
			logger.WithError(err).Error("Failed to send recording summary via WxPusher")
		}
	}
}
