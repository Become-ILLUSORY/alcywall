package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------- notify

func notify(ctx context.Context, cfg Config, log *logger, title, body, icon string) {
	if !cfg.Notify {
		return
	}

	// Noctalia owns notifications on this desktop; prefer its IPC so the toast
	// looks native and lands in the shell's history.
	if noctaliaAvailable(ctx, cfg) {
		payload, err := json.Marshal(map[string]any{
			"app_name":   cfg.NotifyApp,
			"summary":    title,
			"body":       body,
			"urgency":    "low",
			"timeout_ms": 4000,
			"icon":       icon,
		})
		if err == nil {
			if _, err := runCmd(ctx, 5*time.Second, noctaliaBin(cfg), "msg", "notification-show", string(payload)); err == nil {
				return
			}
		}
	}

	// Fallback for sessions without Noctalia.
	if !hasBinary("notify-send") {
		log.Debugf("no notification backend (noctalia IPC / notify-send missing)")
		return
	}
	args := []string{
		"--app-name=" + cfg.NotifyApp,
		"--replace-id=999",
		"--expire-time=4000",
		"--urgency=low",
	}
	if icon != "" {
		args = append(args, "--icon="+icon)
	}
	args = append(args, title, body)
	if _, err := runCmd(ctx, 5*time.Second, "notify-send", args...); err != nil {
		log.Debugf("notify-send: %v", err)
	}
}

// ---------------------------------------------------------------- colour sampling

// averageHex reads the average colour of an image. It uses ImageMagick when
// present (handles every format the API serves) and returns "" otherwise, so
// the caller simply skips the sync instead of failing the run.
func averageHex(ctx context.Context, path string) string {
	bin := ""
	args := []string{}
	if hasBinary("magick") {
		bin = "magick"
		args = []string{"convert", path, "-resize", "1x1!", "-format", "%[hex:p{0,0}]", "info:"}
	} else if hasBinary("convert") {
		bin = "convert"
		args = []string{path, "-resize", "1x1!", "-format", "%[hex:p{0,0}]", "info:"}
	} else {
		return ""
	}
	out, err := runCmd(ctx, 10*time.Second, bin, args...)
	if err != nil {
		return ""
	}
	hex := strings.TrimSpace(string(out))
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) < 6 {
		return ""
	}
	return "#" + strings.ToUpper(hex[:6])
}

// ---------------------------------------------------------------- detached start

// startDetached launches a long-running helper (swaybg) that must outlive this
// process, in its own session so it is not killed with us.
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// copyFile copies src to dst, creating parent directories.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(dirOf(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func dirOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "."
	}
	return p[:i]
}
