//go:build dev

package servers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/bililive-go/bililive-go/src/tools"
)

func registerDevDebugRoutes(apiRoute *mux.Router) {
	// POST /api/debug/ffmpeg/force-state — 强制设置 FFmpeg 状态，仅用于 e2e 测试
	apiRoute.HandleFunc("/debug/ffmpeg/force-state", forceFFmpegStateHandler).Methods("POST")
	// POST /api/debug/ffmpeg/reinit — 重新触发 FFmpeg 异步初始化流程，仅用于 e2e 测试
	// （配合删除工具缓存目录，可让下载流程在同一进程内重新走一遍）
	apiRoute.HandleFunc("/debug/ffmpeg/reinit", reinitFFmpegHandler).Methods("POST")
}

func reinitFFmpegHandler(w http.ResponseWriter, r *http.Request) {
	// 不使用 r.Context()：异步初始化的生命周期应跟随进程而非本次请求
	tools.FFmpegAsyncInit(context.Background())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

func forceFFmpegStateHandler(w http.ResponseWriter, r *http.Request) {
	var req tools.FFmpegStatus
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	tools.ForceFFmpegStatus(req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}
