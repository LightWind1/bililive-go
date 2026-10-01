package utils

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/live"
	blog "github.com/bililive-go/bililive-go/src/log"
	"github.com/kira1928/remotetools"
)

func init() {
	ConnCounterManager = ConnCounterManagerType{}
	ConnCounterManager.bcMap = make(map[string]*ByteCounter)
}

func GetFFmpegPath(ctx context.Context) (string, error) {
	var path string
	if cfg := configs.GetCurrentConfig(); cfg != nil {
		path = cfg.FfmpegPath
	}
	if path != "" {
		return validateConfiguredFFmpegPath(path)
	}

	// try to get from remotetools
	if toolFfmpeg, err := remotetools.Get().GetTool("ffmpeg"); err == nil {
		if toolFfmpeg.DoesToolExist() {
			return toolFfmpeg.GetToolPath(), nil
		}
	}

	return LookupSystemFFmpeg()
}

// EnvIgnoreSystemFFmpeg 设置该环境变量后跳过系统 PATH 中的 FFmpeg 查找，
// 强制走 remotetools 下载流程。仅用于 e2e 测试模拟"无 FFmpeg"环境
// （CI 机器普遍预装 ffmpeg，无法用真实 PATH 构造该场景）。
const EnvIgnoreSystemFFmpeg = "BILILIVE_IGNORE_SYSTEM_FFMPEG"

// LookupSystemFFmpeg 在系统 PATH 中查找 ffmpeg，找不到时回退查找工作目录下的 ./ffmpeg
// （允许把 ffmpeg 和主程序放在同一目录）。
// 注意不能只处理 exec.ErrDot：Unix 下当前目录不在 PATH 中，
// LookPath 返回 ErrNotFound 而非 ErrDot，因此任何失败都应尝试 ./ffmpeg。
func LookupSystemFFmpeg() (string, error) {
	if os.Getenv(EnvIgnoreSystemFFmpeg) != "" {
		return "", errors.New("system ffmpeg lookup disabled by " + EnvIgnoreSystemFFmpeg)
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		path, err = exec.LookPath("./ffmpeg")
	}
	return path, err
}

// validateConfiguredFFmpegPath 校验用户显式配置的 ffmpeg_path。
// 配置路径与自动查找不同：只要用户配置了该路径，实际录制就不会回退到
// remotetools / 系统 PATH，因此这里必须保证它是可执行文件路径而不是目录。
func validateConfiguredFFmpegPath(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("ffmpeg_path is a directory: %s", path)
	}
	return path, nil
}

// GetFFmpegPathForLive 获取特定直播间的FFmpeg路径（使用解析后的配置）
func GetFFmpegPathForLive(ctx context.Context, liveInstance live.Live) (string, error) {
	cfg := configs.GetCurrentConfig()
	if cfg == nil {
		return GetFFmpegPath(ctx)
	}

	// 获取解析后的配置
	room, err := cfg.GetLiveRoomByUrl(liveInstance.GetRawUrl())
	var ffmpegPath string
	if err == nil {
		platformKey := configs.GetPlatformKeyFromUrl(liveInstance.GetRawUrl())
		resolvedConfig := cfg.ResolveConfigForRoom(room, platformKey)
		ffmpegPath = resolvedConfig.FfmpegPath
	} else {
		// 回退到全局配置
		ffmpegPath = cfg.FfmpegPath
	}

	if ffmpegPath != "" {
		return validateConfiguredFFmpegPath(ffmpegPath)
	}

	// 如果没有配置FFmpeg路径，尝试从环境变量或 remotetools 查找
	return GetFFmpegPath(ctx)
}

func IsFFmpegExist(ctx context.Context) bool {
	_, err := GetFFmpegPath(ctx)
	return err == nil
}

func GetMd5String(b []byte) string {
	md5Obj := md5.New()
	md5Obj.Write(b)
	return hex.EncodeToString(md5Obj.Sum(nil))
}

var (
	lowercaseRunes = []rune("abcdefghijklmnopqrstuvwxyz")
	uppercaseRunes = []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	lettersRunes   = append(lowercaseRunes, uppercaseRunes...)
	digitsRunes    = []rune("0123456789")
	allRunes       = append(lettersRunes, digitsRunes...)
)

func GenRandomName(n int) string {
	b := make([]rune, n)
	b[0] = lowercaseRunes[rand.Intn(len(lowercaseRunes))]
	for i := 1; i < n; i++ {
		b[i] = allRunes[rand.Intn(len(allRunes))]
	}
	return string(b)
}

func GenRandomString(length int, validChars string) string {
	b := make([]string, length)
	chars := strings.Split(validChars, "")
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return strings.Join(b, "")
}

func Match1(re, str string) string {
	reg, err := regexp.Compile(re)
	if err != nil {
		return ""
	}
	match := reg.FindStringSubmatch(str)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func GenUrls(strs ...string) ([]*url.URL, error) {
	urls := make([]*url.URL, 0, len(strs))
	for _, str := range strs {
		u, err := url.Parse(str)
		if err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, nil
}

func GenUrlInfos(urls []*url.URL, headersForDownloader map[string]string) []*live.StreamUrlInfo {
	infos := make([]*live.StreamUrlInfo, 0, len(urls))
	for _, u := range urls {
		infos = append(infos, &live.StreamUrlInfo{
			Url:                  u,
			Name:                 "",
			Description:          "",
			Resolution:           0,
			Vbitrate:             0,
			HeadersForDownloader: headersForDownloader,
		})
	}
	return infos
}

func PrintStack() {
	blog.GetLogger().Debugf("%s", string(debug.Stack()))
}

func ExecCommands(commands [][]string) error {
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, command := range commands {
		err := ExecCommandInDir(command, pwd)
		if err != nil {
			return err
		}
	}
	return nil
}

func ExecCommand(command []string) error {
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return ExecCommandInDir(command, pwd)
}

func ExecCommandsInDir(commands [][]string, dir string) error {
	for _, command := range commands {
		err := ExecCommandInDir(command, dir)
		if err != nil {
			return err
		}
	}
	return nil
}

func ExecCommandInDir(args []string, dir string) error {
	name := args[0]
	cmd := exec.Command(name, args[1:]...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	blog.GetLogger().Info(cmd.String())
	return cmd.Run()
}

const (
	KB = 1024
	MB = 1024 * KB
	GB = 1024 * MB
)

func FormatBytes(bytes int64) string {
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
