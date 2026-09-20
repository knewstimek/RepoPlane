//go:build !windows

package runner

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func validateProcessExecutable(string) error { return nil }

func newProcessCommand(ctx context.Context, executable string, argv []string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, executable, argv...), nil
}

func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(process *os.Process) error {
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return process.Kill()
}
