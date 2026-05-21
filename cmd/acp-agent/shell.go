package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// commandResult captures structured client-side shell output.
type commandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Signal   string
}

func (c *client) runClientShellCommand(ctx context.Context, command string) (runLuaResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return runLuaResult{}, fmt.Errorf("command must be a non-empty string")
	}
	c.mu.Lock()
	cwd := c.state.Cwd
	c.mu.Unlock()
	approvedCommand, err := c.confirmShellCommand(command)
	if err != nil {
		return runLuaResult{}, fmt.Errorf("request permission: %w", err)
	}
	res, err := runShell(ctx, approvedCommand, cwd)
	if err != nil {
		return runLuaResult{}, err
	}
	return runLuaResult{Output: formatCommandOutput(res)}, nil
}

func (c *client) confirmShellCommand(command string) (string, error) {
	if c.commandAllowed(command) {
		return command, nil
	}
	c.mu.Lock()
	mode := c.opts.Permission
	c.mu.Unlock()
	switch mode {
	case "allow", "y", "yes":
		return command, nil
	case "cancel", "cancelled":
		return "", fmt.Errorf("command cancelled by user")
	case "reject":
		return "", fmt.Errorf("command rejected by user")
	case "", "prompt":
	default:
		return "", fmt.Errorf("command rejected by user")
	}
	choice, err := c.choose("permission requested: Run command", "command: "+command, []choiceOption{
		{Key: "1", Label: "yes"},
		{Key: "2", Label: "yes (same command)"},
		{Key: "3", Label: "no"},
		{Key: "4", Label: "other command"},
		{Key: "0", Label: "yolo"},
	})
	if err != nil {
		return "", err
	}
	switch choice {
	case "1", "y", "yes":
		return command, nil
	case "2":
		c.rememberAllowedCommand(command)
		return command, nil
	case "4":
		fmt.Fprint(c.stderr, "command> ")
		next, err := c.stdin.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		next = strings.TrimSpace(next)
		if next == "" {
			return "", fmt.Errorf("command rejected by user")
		}
		return next, nil
	case "0":
		c.setPermissionMode("allow")
		return command, nil
	default:
		return "", fmt.Errorf("command rejected by user")
	}
}

// runShell executes `sh -c command` in cwd and captures stdout/stderr.
func runShell(ctx context.Context, command, cwd string) (commandResult, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return commandResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return commandResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return commandResult{}, err
	}
	outBytes, _ := io.ReadAll(stdout)
	errBytes, _ := io.ReadAll(stderr)
	wErr := cmd.Wait()
	res := commandResult{Stdout: string(outBytes), Stderr: string(errBytes)}
	if wErr == nil {
		res.ExitCode = 0
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(wErr, &ee) {
		res.ExitCode = ee.ExitCode()
		if ee.ProcessState != nil && ee.ProcessState.ExitCode() == -1 {
			res.Signal = ee.ProcessState.String()
		}
		return res, nil
	}
	return res, wErr
}

func formatCommandOutput(r commandResult) string {
	lines := []string{fmt.Sprintf("Exit code: %d", r.ExitCode)}
	if r.Signal != "" {
		lines = append(lines, "Signal: "+r.Signal)
	}
	stdout := r.Stdout
	if stdout == "" {
		stdout = "(empty)"
	}
	stderr := r.Stderr
	if stderr == "" {
		stderr = "(empty)"
	}
	lines = append(lines, "", "STDOUT:", stdout, "", "STDERR:", stderr)
	return strings.Join(lines, "\n")
}
