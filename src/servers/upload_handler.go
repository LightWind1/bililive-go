package servers

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/gorilla/mux"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
)

// uploadResult 手动上传的响应结构
type uploadResult struct {
	Enqueued int      `json:"enqueued"`
	Skipped  []string `json:"skipped"`
	TaskIDs  []int64  `json:"task_ids"`
}

// RegisterUploadHandlers 注册手动上传相关的 HTTP 处理器
// 通过 Pipeline 系统执行上传，与自动上传行为完全一致
func RegisterUploadHandlers(r *mux.Router, pm *pipeline.Manager) {
	if pm == nil {
		return
	}

	r.HandleFunc("/file/upload", makeUploadFileHandler(pm)).Methods("POST")
	r.HandleFunc("/batch/file/upload", makeBatchUploadFilesHandler(pm)).Methods("POST")
}

// makeUploadFileHandler 单文件上传到云盘（通过 Pipeline）
func makeUploadFileHandler(pm *pipeline.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, commonResp{ErrMsg: "请求参数错误"})
			return
		}
		if req.Path == "" {
			writeJSON(w, commonResp{ErrMsg: "文件路径不能为空"})
			return
		}

		// 路径校验（复用 handler.go 中的 getSafePath）
		cfg := configs.GetCurrentConfig()
		if cfg == nil {
			writeJSON(w, commonResp{ErrMsg: "配置未初始化"})
			return
		}
		absPath, err := getSafePath(cfg.OutPutPath, req.Path)
		if err != nil {
			writeJSON(w, commonResp{ErrMsg: "无效路径: " + err.Error()})
			return
		}

		enqueued, skipped, taskIDs, err := pm.EnqueueUploadTask([]string{absPath})
		if err != nil {
			writeJSON(w, commonResp{ErrMsg: err.Error()})
			return
		}

		writeJSON(w, commonResp{Data: uploadResult{
			Enqueued: enqueued,
			Skipped:  skipped,
			TaskIDs:  taskIDs,
		}})
	}
}

// makeBatchUploadFilesHandler 批量上传文件到云盘（通过 Pipeline）
func makeBatchUploadFilesHandler(pm *pipeline.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, commonResp{ErrMsg: "请求参数错误"})
			return
		}

		// 批量路径校验
		cfg := configs.GetCurrentConfig()
		if cfg == nil {
			writeJSON(w, commonResp{ErrMsg: "配置未初始化"})
			return
		}

		absPaths := make([]string, 0, len(req.Paths))
		skipped := []string{}
		for _, p := range req.Paths {
			absPath, err := getSafePath(cfg.OutPutPath, p)
			if err != nil {
				skipped = append(skipped, filepath.Base(p)+" - 路径越权")
				continue
			}
			absPaths = append(absPaths, absPath)
		}

		// 如果所有路径都校验失败，直接返回
		if len(absPaths) == 0 {
			writeJSON(w, commonResp{Data: uploadResult{
				Enqueued: 0,
				Skipped:  skipped,
				TaskIDs:  []int64{},
			}})
			return
		}

		enqueued, moreSkipped, taskIDs, err := pm.EnqueueUploadTask(absPaths)
		if err != nil {
			writeJSON(w, commonResp{ErrMsg: err.Error()})
			return
		}
		skipped = append(skipped, moreSkipped...)

		writeJSON(w, commonResp{Data: uploadResult{
			Enqueued: enqueued,
			Skipped:  skipped,
			TaskIDs:  taskIDs,
		}})
	}
}
