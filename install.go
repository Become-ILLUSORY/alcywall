package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const serviceTemplate = `[Unit]
Description=alcywall — random 二次元 wallpaper from 栗次元 (t.alcy.cc)
After=graphical-session.target
Wants=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=oneshot
ExecStart=%s next
%s`

const timerTemplate = `[Unit]
Description=Rotate the 栗次元 wallpaper (alcywall)

[Timer]
OnBootSec=%s
OnUnitActiveSec=%s
RandomizedDelaySec=%s
AccuracySec=1min
Unit=alcywall.service

[Install]
WantedBy=timers.target
`

func unitDir() string {
	return filepath.Join(configHome(), "systemd", "user")
}

func passthrough(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func cmdInstall(ctx context.Context, args []string) error {
	o := newOpts()
	var bin, extraEnv string
	fs := newFlagSet("install", o, func(f *flag.FlagSet) {
		f.StringVar(&bin, "bin", "", "alcywall binary to run (default: this executable)")
		f.DurationVar(&o.interval, "interval", 30*time.Minute, "rotation interval")
		f.DurationVar(&o.jitter, "random-delay", 2*time.Minute, "max random extra delay per cycle")
		f.StringVar(&o.onBoot, "on-boot", "2min", "delay before the first run after login")
		f.StringVar(&extraEnv, "env", "", "extra Environment= entries, comma separated KEY=VALUE")
	})
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	log := newLogger(o.verbose)
	cfg, err := buildConfig(o)
	if err != nil {
		return err
	}

	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot locate this binary, pass -bin: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			bin = resolved
		} else {
			bin = exe
		}
	}
	if !fileExists(bin) {
		return fmt.Errorf("binary %s not found", bin)
	}

	envLines := ""
	for _, kv := range strings.Split(extraEnv, ",") {
		if kv = strings.TrimSpace(kv); kv != "" {
			envLines += "Environment=" + kv + "\n"
		}
	}
	if dir := resolveDirectory(ctx, cfg); dir != "" && !strings.Contains(envLines, "ALCYWALL_DIR") {
		envLines += "Environment=ALCYWALL_DIR=" + dir + "\n"
	}
	if cat := cfg.Category; cat != "" && !strings.Contains(envLines, "ALCYWALL_CATEGORY") {
		envLines += "Environment=ALCYWALL_CATEGORY=" + cat + "\n"
	}

	if !hasBinary("systemctl") {
		return errors.New("systemctl not found — run 'alcywall watch -interval 30m' from your compositor autostart instead")
	}
	dir := unitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	svc := filepath.Join(dir, "alcywall.service")
	tmr := filepath.Join(dir, "alcywall.timer")
	if err := os.WriteFile(svc, []byte(fmt.Sprintf(serviceTemplate, fmt.Sprintf("%q", bin), envLines)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(tmr, []byte(fmt.Sprintf(timerTemplate, o.onBoot, o.interval.String(), o.jitter.String())), 0o644); err != nil {
		return err
	}
	log.Infof("wrote %s and %s", svc, tmr)

	if err := passthrough(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := passthrough(ctx, "systemctl", "--user", "enable", "--now", "alcywall.timer"); err != nil {
		return err
	}
	_ = passthrough(ctx, "systemctl", "--user", "list-timers", "alcywall.timer", "--no-pager")
	fmt.Printf("\nNext steps:\n" +
		"  manual run : systemctl --user start alcywall.service\n" +
		"  logs       : journalctl --user -u alcywall.service -n 50\n" +
		"  hotkey     : bind an input in niri to: alcywall next -silent\n")
	return nil
}

func cmdUninstall(ctx context.Context, args []string) error {
	o := newOpts()
	fs := newFlagSet("uninstall", o, nil)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	log := newLogger(o.verbose)
	if hasBinary("systemctl") {
		_ = passthrough(ctx, "systemctl", "--user", "disable", "--now", "alcywall.timer")
	}
	removed := 0
	for _, name := range []string{"alcywall.service", "alcywall.timer"} {
		p := filepath.Join(unitDir(), name)
		if err := os.Remove(p); err == nil {
			removed++
			log.Infof("removed %s", p)
		}
	}
	if hasBinary("systemctl") {
		_ = passthrough(ctx, "systemctl", "--user", "daemon-reload")
	}
	if removed == 0 {
		fmt.Println("nothing installed")
	}
	return nil
}
