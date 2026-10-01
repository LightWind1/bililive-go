//go:build !windows

package launcher

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcAttr 设置独立进程组，强制终止时可通过 kill(-pgid) 同时清理所有子进程
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 杀掉整个进程组（包含 bililive-tools 等子进程），避免端口残留
func killProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}

	err := syscall.Kill(-pid, syscall.SIGKILL)
	// 主程序及其所有后代均已退出时，进程组不存在是正常状态。
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
