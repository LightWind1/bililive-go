//go:build !windows

package launcher

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const launcherProcessHelperMode = "BILILIVE_LAUNCHER_PROCESS_HELPER"

// TestLauncherProcessHelper 由 TestWaitForMainProgramCleansDescendants 作为子进程调用。
func TestLauncherProcessHelper(t *testing.T) {
	mode := os.Getenv(launcherProcessHelperMode)
	if mode == "" {
		return
	}

	statePath := os.Getenv("BILILIVE_LAUNCHER_HELPER_STATE")
	switch mode {
	case "leader":
		cmd := exec.Command(os.Args[0], "-test.run=^TestLauncherProcessHelper$")
		cmd.Env = append(os.Environ(),
			launcherProcessHelperMode+"=listener",
			"BILILIVE_LAUNCHER_HELPER_STATE="+statePath,
		)
		if err := cmd.Start(); err != nil {
			t.Fatalf("启动监听子进程失败: %v", err)
		}

		// 确保后代进程已经监听端口，再让主程序退出。
		waitForHelperState(t, statePath)
		return
	case "listener":
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("监听测试端口失败: %v", err)
		}
		defer listener.Close()

		state := fmt.Sprintf("%s|%d", listener.Addr().String(), os.Getpid())
		if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
			t.Fatalf("写入测试状态失败: %v", err)
		}

		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	default:
		t.Fatalf("未知辅助进程模式: %s", mode)
	}
}

func TestWaitForMainProgramCleansDescendants(t *testing.T) {
	statePath := t.TempDir() + "/listener.state"
	cmd := exec.Command(os.Args[0], "-test.run=^TestLauncherProcessHelper$")
	cmd.Env = append(os.Environ(),
		launcherProcessHelperMode+"=leader",
		"BILILIVE_LAUNCHER_HELPER_STATE="+statePath,
	)
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动主测试进程失败: %v", err)
	}

	runner := &Runner{
		mainProcess: cmd,
		mainPID:     cmd.Process.Pid,
		processDone: make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		close(runner.processDone)
	}()

	state := waitForHelperState(t, statePath)
	parts := strings.Split(state, "|")
	if len(parts) != 2 {
		t.Fatalf("无效的测试状态: %q", state)
	}
	address := parts[0]
	childPID, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("无效的子进程 PID: %q", parts[1])
	}
	defer func() {
		if process, findErr := os.FindProcess(childPID); findErr == nil {
			_ = process.Kill()
		}
	}()

	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("后代进程未监听测试端口: %v", err)
	}
	_ = conn.Close()

	// 主程序已经退出，但监听端口的后代进程仍在同一进程组中。
	// waitForMainProgram 必须在返回前清理该进程组。
	runner.waitForMainProgram()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp4", address, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waitForMainProgram 返回后，后代进程仍占用端口 %s", address)
}

func waitForHelperState(t *testing.T, statePath string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(statePath)
		if err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待辅助进程状态超时: %s", statePath)
	return ""
}
