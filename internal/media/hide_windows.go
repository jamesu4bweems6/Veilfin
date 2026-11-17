//go:build windows

package media

import (
	"os/exec"
	"syscall"
)

// hideWindow 避免在 Windows 上为每次 ffmpeg 调用弹出控制台窗口。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
