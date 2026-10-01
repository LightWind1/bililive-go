package servers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bililive-go/bililive-go/src/instance"
	"github.com/bililive-go/bililive-go/src/tools"
)

// getFFmpegStatusHandler 返回当前 FFmpeg 就绪状态
// GET /api/ffmpeg/status
func getFFmpegStatusHandler(w http.ResponseWriter, r *http.Request) {
	status := tools.GetFFmpegStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// retryFFmpegHandler 重新触发 FFmpeg 异步检测/下载流程。
// 用于下载失败（error）或未找到（not_found）后，让用户在不重启后端的情况下重试，
// 而不会永久停留在失败横幅上。
// FFmpegAsyncInit 自带并发守卫：若检测/下载已在进行，本次调用为无操作。
// POST /api/ffmpeg/retry
func retryFFmpegHandler(w http.ResponseWriter, r *http.Request) {
	// 不直接使用 r.Context()：异步初始化的生命周期应跟随进程而非本次请求。
	// 但也不能直接用 context.Background()，否则进程退出时无法取消下载/写文件流程。
	appCtx := context.Background()
	if inst := instance.GetInstance(r.Context()); inst != nil && inst.Ctx != nil {
		appCtx = inst.Ctx
	}
	tools.FFmpegAsyncInit(appCtx)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}
