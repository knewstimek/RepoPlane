//go:build windows

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func newProcessCommand(ctx context.Context, executable string, argv []string) (*exec.Cmd, error) {
	extension := strings.ToLower(filepath.Ext(executable))
	if extension != ".cmd" && extension != ".bat" {
		return exec.CommandContext(ctx, executable, argv...), nil
	}
	commandInterpreter := os.Getenv("ComSpec")
	if commandInterpreter == "" {
		var err error
		commandInterpreter, err = exec.LookPath("cmd.exe")
		if err != nil {
			return nil, errors.New("Windows command interpreter is unavailable")
		}
	}
	// cmd.exe does not use CommandLineToArgvW escaping. Give it a raw command
	// line with one quoted token per manifest argv element.
	parts := make([]string, 0, len(argv)+1)
	executableToken, err := quoteBatchToken(executable)
	if err != nil {
		return nil, err
	}
	parts = append(parts, executableToken)
	for _, argument := range argv {
		token, err := quoteBatchToken(argument)
		if err != nil {
			return nil, err
		}
		parts = append(parts, token)
	}
	command := exec.CommandContext(ctx, commandInterpreter)
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/d /s /v:off /c "` + strings.Join(parts, " ") + `"`}
	return command, nil
}

func validateProcessExecutable(path string) error {
	extension := strings.ToLower(filepath.Ext(path))
	if extension == ".ps1" {
		return &UnsupportedScriptTypeError{Extension: extension}
	}
	return nil
}

func quoteBatchToken(value string) (string, error) {
	if strings.ContainsAny(value, "\"%\r\n\x00") {
		return "", errors.New("batch arguments cannot contain quotes, percent signs, or control characters")
	}
	return `"` + value + `"`, nil
}

func configureProcess(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

func terminateProcessTree(process *os.Process) error {
	command := exec.Command("taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Run(); err == nil {
		return nil
	}
	return process.Kill()
}
