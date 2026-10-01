package pipeline

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// UploadPathData 上传路径模板数据
type UploadPathData struct {
	Platform string
	HostName string
	RoomName string
	FileName string
	Ext      string
}

// DefaultUploadPathFuncs 返回上传路径模板的默认函数集
// nowFn 返回当前时间（可从 RecordInfo.StartTime 或 time.Now() 传入）
func DefaultUploadPathFuncs(nowFn func() time.Time) template.FuncMap {
	return template.FuncMap{
		"date": func(format string, t time.Time) string {
			return t.Format(format)
		},
		"now": nowFn,
		"trimSuffix": func(suffix, s string) string {
			return strings.TrimSuffix(s, suffix)
		},
		"ext": func(path string) string {
			return filepath.Ext(path)
		},
		"filenameFilter": func(s string) string {
			replacer := strings.NewReplacer(
				"/", "_", "\\", "_", ":", "_", "*", "_",
				"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
			)
			return replacer.Replace(s)
		},
	}
}

// RenderUploadPath 渲染上传目标路径
// pathTemplate 为空时使用默认路径 /录播归档/{Platform}/{HostName}/{FileName}
// 返回空字符串表示模板解析失败
func RenderUploadPath(pathTemplate string, data UploadPathData, nowFn func() time.Time) (string, error) {
	if pathTemplate == "" {
		path := fmt.Sprintf("/录播归档/%s/%s/%s", data.Platform, data.HostName, data.FileName)
		return path, nil
	}

	funcMap := DefaultUploadPathFuncs(nowFn)

	tmpl, err := template.New("path").Funcs(funcMap).Parse(pathTemplate)
	if err != nil {
		return "", fmt.Errorf("上传路径模板解析失败: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("上传路径模板执行失败: %w", err)
	}

	return buf.String(), nil
}
