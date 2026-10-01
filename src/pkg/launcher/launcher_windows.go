//go:build windows

package launcher

import "os/exec"

// setProcAttr 在 Windows 上无需设置 Unix 进程组。
func setProcAttr(cmd *exec.Cmd) {}

// killProcessGroup 在 Windows 上无需处理 Unix 进程组。
// bililive-tools 等子进程由主程序创建的 Job Object 负责随主程序退出。
func killProcessGroup(pid int) error { return nil }
