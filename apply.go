package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backend names accepted by -backend / apply_backend.
const (
	backendAuto     = "auto"
	backendNoctalia = "noctalia"
	backendNiri     = "niri"
	backendNone     = "none"
)

// applyWallpaper pushes `path` to whatever owns the desktop wallpaper.
//
// Noctalia is the shell that actually renders wallpapers, so it is the primary
// backend: `noctalia msg wallpaper-set [connector] <path>`. niri itself has no
// "set wallpaper" IPC (it is a compositor; the image belongs to the shell or to
// a layer-shell tool), so the niri side of the integration is the solid
// background colour it paints per output — synced so the gap behind the shell
// matches the image during transitions.
func applyWallpaper(ctx context.Context, cfg Config, path string, log *logger) error {
	switch cfg.ApplyBackend {
	case backendNone:
		log.Debugf("apply backend is none, leaving the wallpaper alone")
		return nil
	case backendNoctalia:
		if err := applyViaNoctalia(ctx, cfg, path, log); err != nil {
			return err
		}
	case backendNiri:
		return applyViaNiriOnly(ctx, cfg, path, log)
	default: // auto
		if noctaliaAvailable(ctx, cfg) {
			if err := applyViaNoctalia(ctx, cfg, path, log); err != nil {
				log.Warnf("noctalia: %v", err)
			}
		} else if hasBinary("swaybg") && os.Getenv("NIRI_SOCKET") != "" {
			if err := applyViaSwaybg(ctx, cfg, path, log); err != nil {
				log.Warnf("swaybg: %v", err)
			}
		} else {
			return fmt.Errorf("no wallpaper backend available: Noctalia IPC is not answering and swaybg is not installed (pass -backend niri for a colour-only fallback)")
		}
	}

	if cfg.UseNiri {
		syncNiriBackground(ctx, cfg, path, log)
	}
	return nil
}

// applyViaNoctalia talks to the shell over its own CLI, which is the documented
// stable surface: `noctalia msg wallpaper-set [connector] <path>`.
//
// From the Noctalia v5 IPC docs (Media & UI → Wallpaper):
//
//	wallpaper-set <path>              all monitors + the persisted default
//	wallpaper-set <connector> <path>  one monitor, others untouched
//
// Paths accept an image path (~ expanded) or color:#RRGGBB; non-colour paths
// must exist at call time.
func applyViaNoctalia(ctx context.Context, cfg Config, path string, log *logger) error {
	bin := noctaliaBin(cfg)
	if !hasBinary(bin) {
		return fmt.Errorf("%s not found in PATH (install Noctalia or pass -backend niri)", bin)
	}
	args := []string{"msg", "wallpaper-set"}
	if cfg.Connector != "" {
		args = append(args, cfg.Connector)
	}
	args = append(args, path)
	out, err := runCmd(ctx, 15*time.Second, bin, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w %s", bin, strings.Join(args, " "), err, truncate(string(out), 200))
	}
	if msg := strings.TrimSpace(string(out)); msg != "" && !strings.HasPrefix(msg, "ok") {
		log.Debugf("noctalia: %s", truncate(msg, 160))
	}
	log.Debugf("applied via %s", bin)
	return nil
}

// applyViaNiriOnly is the no-shell fallback: niri cannot display an image, so we
// only sync the solid background colour derived from it and tell the user.
func applyViaNiriOnly(ctx context.Context, cfg Config, path string, log *logger) error {
	if !cfg.UseNiri {
		return fmt.Errorf("backend niri needs niri_sync_background enabled")
	}
	if err := syncNiriBackground(ctx, cfg, path, log); err != nil {
		return err
	}
	log.Infof("niri cannot display image wallpapers itself; synced the background colour only.")
	log.Infof("Install Noctalia (or swaybg) for the actual image.")
	return nil
}

// syncNiriBackground derives the average colour of the wallpaper and writes it
// into the niri config, then asks niri to reload.
func syncNiriBackground(ctx context.Context, cfg Config, path string, log *logger) error {
	if os.Getenv("NIRI_SOCKET") == "" && !hasBinary("niri") {
		log.Debugf("niri not detected, skipping background sync")
		return nil
	}
	hex := averageHex(ctx, path)
	if hex == "" {
		log.Debugf("no colour sampler available (install imagemagick), skipping niri background sync")
		return nil
	}
	return niriBackgroundColor(ctx, cfg, hex, log)
}

// applyViaSwaybg restarts a swaybg instance with the new image. Only used when
// Noctalia is absent but the user still wants image wallpapers on niri.
func applyViaSwaybg(ctx context.Context, cfg Config, path string, log *logger) error {
	// swaybg is a long-running process; kill the previous one and start fresh.
	if _, err := runCmd(ctx, 3*time.Second, "pkill", "-x", "swaybg"); err != nil {
		log.Debugf("pkill swaybg: %v", err)
	}
	args := []string{"-i", path}
	if cfg.Connector != "" {
		args = []string{"-o", cfg.Connector, "-i", path}
	}
	if err := startDetached("swaybg", args...); err != nil {
		return err
	}
	log.Debugf("started swaybg for %s", filepath.Base(path))
	return nil
}

// ---------------------------------------------------------------- niri config

// niriConfigPath resolves the niri config file, honouring $NIRI_CONFIG.
func niriConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("NIRI_CONFIG")); v != "" {
		return expandUser(v)
	}
	if v := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); v != "" {
		return filepath.Join(v, "niri", "config.kdl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "niri", "config.kdl")
}

// niriBackgroundColor writes `hex` as the background colour for the output
// (or for every output when no connector is pinned) and reloads niri.
func niriBackgroundColor(ctx context.Context, cfg Config, hex string, log *logger) error {
	path := niriConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate the niri config file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no niri config at %s", path)
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	targets := []string{cfg.Connector}
	if cfg.Connector == "" {
		targets = detectConnectors(ctx)
		if len(targets) == 0 {
			targets = []string{""}
		}
	}

	updated := string(data)
	changed := false
	for _, connector := range targets {
		var did bool
		updated, did = upsertNiriBackgroundColor(updated, connector, hex)
		changed = changed || did
	}
	if !changed {
		log.Debugf("niri: background-color already %s", hex)
		return nil
	}
	if err := writeFileAtomic(path, []byte(updated), 0o644); err != nil {
		return err
	}
	if _, err := runCmd(ctx, 5*time.Second, "niri", "msg", "action", "load-config-file"); err != nil {
		// Older niri reloads through its file watcher on its own.
		log.Debugf("niri reload: %v (the config watcher picks it up anyway)", err)
	}
	log.Debugf("niri: background-color -> %s", hex)
	return nil
}

// writeFileAtomic replaces path with data via a temp file + rename.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".alcywall.tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// stripComment removes a trailing // comment (KDL uses // and /* */; the former
// is what niri configs use).
func stripComment(s string) string {
	if i := strings.Index(s, "//"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// matchBrace returns the index of the line that closes the block opened at
// openIdx, or -1 when the braces never balance.
func matchBrace(lines []string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(lines); i++ {
		t := stripComment(strings.TrimSpace(lines[i]))
		depth += strings.Count(t, "{") - strings.Count(t, "}")
		if depth <= 0 {
			return i
		}
	}
	return -1
}

// findOutputBlock locates the line range of `output "<connector>" { … }`.
// Returns (-1, -1, "") when there is no such block.
func findOutputBlock(lines []string, connector string) (int, int, string) {
	for i, raw := range lines {
		trimmed := stripComment(strings.TrimSpace(raw))
		if !strings.HasPrefix(trimmed, "output ") || !strings.Contains(trimmed, "{") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "output "))
		name = strings.TrimSuffix(name, "{")
		name = strings.Trim(strings.TrimSpace(name), "\"")
		if !strings.EqualFold(name, connector) {
			continue
		}
		end := matchBrace(lines, i)
		if end < 0 {
			return -1, -1, ""
		}
		indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]
		return i, end, indent
	}
	return -1, -1, ""
}

// findChildBlock returns the index of a `<key> {` line that is a direct child of
// the block spanning [start, end].
func findChildBlock(lines []string, start, end int, key string) int {
	depth := 1
	for i := start + 1; i < end; i++ {
		t := stripComment(strings.TrimSpace(lines[i]))
		if t == "" {
			continue
		}
		if depth == 1 {
			fields := strings.Fields(t)
			if len(fields) > 0 && fields[0] == key && strings.Contains(t, "{") {
				return i
			}
		}
		depth += strings.Count(t, "{") - strings.Count(t, "}")
	}
	return -1
}

// findDirectSetting returns the index of a `<key> …` line that is a direct child
// of the block spanning [start, end].
func findDirectSetting(lines []string, start, end int, key string) int {
	depth := 1
	for i := start + 1; i < end; i++ {
		t := stripComment(strings.TrimSpace(lines[i]))
		if t == "" {
			continue
		}
		if depth == 1 && strings.HasPrefix(t, key) {
			rest := strings.TrimPrefix(t, key)
			if rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\"") {
				return i
			}
		}
		depth += strings.Count(t, "{") - strings.Count(t, "}")
	}
	return -1
}

// childIndent returns the leading whitespace used by the first content line
// inside the block, falling back to the block's own indent plus one level.
// Copying the file's existing style avoids mixing tabs into a space-indented
// config (niri itself is agnostic, but a clean diff matters).
func childIndent(lines []string, start, end int, baseIndent string) string {
	for i := start + 1; i < end && i < len(lines); i++ {
		raw := lines[i]
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if ind := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]; ind != "" {
			return ind
		}
	}
	return baseIndent + "\t"
}

// deeperIndent returns the indentation one level deeper than parent, using the
// same step as the parent line.
func deeperIndent(parent string) string {
	if strings.HasPrefix(parent, "\t") || parent == "" {
		return parent + "\t"
	}
	return parent + "    "
}

func insertLine(lines []string, at int, line string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, line)
	out = append(out, lines[at:]...)
	return out
}

// upsertNiriBackgroundColor rewrites the background colour for one output.
//
// Two spellings are accepted by niri and both are handled:
//
//	output "DP-1" { background-color "#112233" }              (legacy, still parsed)
//	output "DP-1" { layout { background-color "#112233" } }   (25.11+ location)
//
// When the block already has a layout { } child, the colour is written there
// (the non-deprecated location); otherwise it goes directly in the output block.
// An empty connector is a no-op — niri has no "all outputs" block, so the caller
// expands it to the connected connector names instead.
func upsertNiriBackgroundColor(cfg, connector, hex string) (string, bool) {
	if connector == "" {
		return cfg, false
	}
	lines := strings.Split(cfg, "\n")
	setting := fmt.Sprintf("background-color \"%s\"", hex)

	start, end, indent := findOutputBlock(lines, connector)
	if start < 0 {
		// No block yet: append a fresh one using the file's own style.
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines,
			fmt.Sprintf("output %q {", connector),
			"\t"+setting,
			"}")
		return strings.Join(lines, "\n"), true
	}
	inner := childIndent(lines, start, end, indent)

	// Prefer the layout { } child when it exists.
	if layoutOpen := findChildBlock(lines, start, end, "layout"); layoutOpen >= 0 {
		if layoutEnd := matchBrace(lines, layoutOpen); layoutEnd > layoutOpen {
			layoutInner := childIndent(lines, layoutOpen, layoutEnd, inner)
			if idx := findDirectSetting(lines, layoutOpen, layoutEnd, "background-color"); idx >= 0 {
				if strings.TrimSpace(lines[idx]) == setting {
					return cfg, false
				}
				lines[idx] = layoutInner + setting
				return strings.Join(lines, "\n"), true
			}
			lines = insertLine(lines, layoutOpen+1, layoutInner+setting)
			return strings.Join(lines, "\n"), true
		}
	}

	if idx := findDirectSetting(lines, start, end, "background-color"); idx >= 0 {
		if strings.TrimSpace(lines[idx]) == setting {
			return cfg, false
		}
		lines[idx] = inner + setting
		return strings.Join(lines, "\n"), true
	}
	lines = insertLine(lines, start+1, inner+setting)
	return strings.Join(lines, "\n"), true
}
