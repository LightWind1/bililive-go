package openlist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// encodePath 对路径的每个段分别编码，保留 / 分隔符
func encodePath(path string) string {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// Client OpenList API 客户端
type Client struct {
	baseURL    string
	mu         sync.RWMutex // 保护 token/username/password 的并发访问
	token      string
	username   string // 用于 token 刷新
	password   string // 用于 token 刷新
	httpClient *http.Client
}

// NewClient 创建 OpenList 客户端
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 0, // 上传可能需要很长时间
		},
	}
}

// SetToken 设置 API Token
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// GetCurrentToken 获取当前 API Token
func (c *Client) GetCurrentToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// SetCredentials 设置用户名密码（用于 token 自动刷新）
func (c *Client) SetCredentials(username, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.username = username
	c.password = password
}

// getTokenForRequest 获取用于请求的 token 副本（内部使用）
func (c *Client) getTokenForRequest() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// refreshToken 刷新 token
func (c *Client) refreshToken(ctx context.Context) error {
	c.mu.RLock()
	username := c.username
	password := c.password
	c.mu.RUnlock()

	if username == "" || password == "" {
		return fmt.Errorf("无法刷新 token: 未配置用户名密码")
	}
	newToken, err := c.GetToken(ctx, username, password)
	if err != nil {
		return fmt.Errorf("刷新 token 失败: %w", err)
	}
	c.SetToken(newToken)
	return nil
}

// APIError 表示 OpenList API 返回的结构化错误
// 用于在 isAuthError 中识别认证失败（code 401/403）
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API 错误 (code %d): %s", e.Code, e.Message)
}

// isAuthError 判断是否为认证错误
// 匹配 HTTP 状态码 401/403 或 OpenList JSON code 401/403
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	// 检查结构化 API 错误
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == 401 || apiErr.Code == 403
	}
	// 兜底：检查错误文本中的 HTTP 状态码
	s := err.Error()
	return strings.Contains(s, "HTTP 401") || strings.Contains(s, "HTTP 403")
}

// withRetry 执行 API 调用，遇到认证错误时自动刷新 token 重试
func (c *Client) withRetry(ctx context.Context, fn func() error) error {
	err := fn()
	if isAuthError(err) {
		if refreshErr := c.refreshToken(ctx); refreshErr == nil {
			return fn()
		}
	}
	return err
}

// Upload 上传文件（使用 PUT /api/fs/put）
func (c *Client) Upload(ctx context.Context, localPath, remotePath string, onProgress func(UploadProgress)) error {
	return c.withRetry(ctx, func() error {
		return c.doUpload(ctx, localPath, remotePath, onProgress)
	})
}

// doUpload 执行实际的上传操作
func (c *Client) doUpload(ctx context.Context, localPath, remotePath string, onProgress func(UploadProgress)) error {
	// 打开本地文件
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("获取文件信息失败: %w", err)
	}

	totalSize := fileInfo.Size()

	// 创建进度追踪 Reader
	progressReader := NewProgressReader(file, totalSize, onProgress)

	// 构建请求
	req, err := http.NewRequestWithContext(ctx, "PUT", c.baseURL+"/api/fs/put", progressReader)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Authorization", c.getTokenForRequest())
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", fmt.Sprintf("%d", totalSize))
	// 对路径的每个段分别编码，保留 / 分隔符
	req.Header.Set("File-Path", encodePath(remotePath))
	req.ContentLength = totalSize

	// 发送请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("上传请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("上传失败 (HTTP %d): %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("解析上传响应失败: %w", err)
	}
	if result.Code != 200 {
		return fmt.Errorf("上传失败: %w", &APIError{Code: result.Code, Message: result.Message})
	}

	return nil
}

// StorageInfo 存储信息
type StorageInfo struct {
	ID        int    `json:"id"`
	MountPath string `json:"mount_path"`
	Driver    string `json:"driver"`
	Status    string `json:"status"`
	Disabled  bool   `json:"disabled"`
}

// ListStorages 列出所有存储
func (c *Client) ListStorages(ctx context.Context) ([]StorageInfo, error) {
	var storages []StorageInfo
	err := c.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/admin/storage/list", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.getTokenForRequest())

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("请求失败: %w", err)
		}
		defer resp.Body.Close()

		var result struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Content []StorageInfo `json:"content"`
			} `json:"data"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}

		if result.Code != 200 {
			return fmt.Errorf("API 错误: %w", &APIError{Code: result.Code, Message: result.Message})
		}

		storages = result.Data.Content
		return nil
	})
	return storages, err
}

// CheckStorageHealth 检查存储健康状态
func (c *Client) CheckStorageHealth(ctx context.Context, storageName string) error {
	return c.withRetry(ctx, func() error {
		body, _ := json.Marshal(map[string]interface{}{
			"path":    "/" + storageName,
			"refresh": true,
		})
		req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/fs/list",
			bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.getTokenForRequest())
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("存储连接失败: %w", err)
		}
		defer resp.Body.Close()

		var result struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}

		if result.Code != 200 {
			return fmt.Errorf("存储不可用: %w", &APIError{Code: result.Code, Message: result.Message})
		}

		return nil
	})
}

// GetToken 获取管理员 Token（通过登录）
func (c *Client) GetToken(ctx context.Context, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/auth/login",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}

	if result.Code != 200 {
		return "", fmt.Errorf("登录失败: %w", &APIError{Code: result.Code, Message: result.Message})
	}

	return result.Data.Token, nil
}

// Mkdir 创建目录
func (c *Client) Mkdir(ctx context.Context, remotePath string) error {
	return c.withRetry(ctx, func() error {
		body, _ := json.Marshal(map[string]string{
			"path": remotePath,
		})
		req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/fs/mkdir",
			bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.getTokenForRequest())
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("请求失败: %w", err)
		}
		defer resp.Body.Close()

		var result struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}

		// 目录已存在也算成功
		if result.Code != 200 && result.Message != "file exists" {
			return fmt.Errorf("创建目录失败: %w", &APIError{Code: result.Code, Message: result.Message})
		}

		return nil
	})
}

// MkdirRecursive 递归创建目录
func (c *Client) MkdirRecursive(ctx context.Context, remotePath string) error {
	// 标准化路径
	remotePath = strings.ReplaceAll(remotePath, "\\", "/")
	if remotePath == "" || remotePath == "/" {
		return nil
	}

	// 分割路径
	parts := strings.Split(strings.Trim(remotePath, "/"), "/")
	if len(parts) == 0 {
		return nil
	}

	// 逐级创建目录
	currentPath := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		currentPath += "/" + part
		if err := c.Mkdir(ctx, currentPath); err != nil {
			return err
		}
	}

	return nil
}

// IsServiceReady 检查 OpenList 服务是否就绪
func (c *Client) IsServiceReady(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/public/settings", nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}
