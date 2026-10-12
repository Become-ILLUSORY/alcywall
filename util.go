package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------- logging

type logger struct {
	verbose bool
	out     io.Writer
}

func newLogger(verbose bool) *logger {
	return &logger{verbose: verbose, out: os.Stderr}
}

func (l *logger) Debugf(format string, args ...any) {
	if l == nil || !l.verbose {
		return
	}
	fmt.Fprintf(l.out, "· "+format+"\n", args...)
}

func (l *logger) Infof(format string, args ...any) {
	if l == nil {
		return
	}
	fmt.Fprintf(l.out, format+"\n", args...)
}

func (l *logger) Warnf(format string, args ...any) {
	if l == nil {
		return
	}
	fmt.Fprintf(l.out, "! "+format+"\n", args...)
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// ---------------------------------------------------------------- process helpers

// runCmd runs a command with a timeout and returns its combined stdout.
func runCmd(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return exec.CommandContext(cctx, name, args...).Output()
}

// hasBinary reports whether name is on PATH.
func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ---------------------------------------------------------------- file helpers

// createTempFile creates a sibling temp file for dst and returns the open file.
// Callers rename it over dst on success; on failure they removeFile(dst).
func createTempFile(dst string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	return os.CreateTemp(filepath.Dir(dst), ".alcywall-*.part")
}

func removeFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// moveFile renames src over dst, falling back to copy+remove across devices.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		removeFile(dst)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// fileExists reports whether path is a regular file.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// uniquePath appends -2, -3, … until the name is free.
func uniquePath(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := filepath.Join(dir, name)
	for i := 2; fileExists(candidate); i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
		if i > 9999 {
			break
		}
	}
	return candidate
}
