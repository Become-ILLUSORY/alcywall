package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------- display detection

// Display describes one connected monitor.
type Display struct {
	Connector string // e.g. DP-1, eDP-1
	Width     int    // physical pixels of the current mode
	Height    int
	Scale     float64
	X, Y      int // logical position in the compositor layout
}

// LogicalWidth is the width the shell renders into (physical / scale).
func (d Display) LogicalWidth() int {
	if d.Scale <= 0 {
		return d.Width
	}
	return int(float64(d.Width)/d.Scale + 0.5)
}

// LogicalHeight is the height the shell renders into (physical / scale).
func (d Display) LogicalHeight() int {
	if d.Scale <= 0 {
		return d.Height
	}
	return int(float64(d.Height)/d.Scale + 0.5)
}

// niriOutput is the JSON shape of `niri msg --json outputs` (one map entry).
type niriOutput struct {
	Name        string `json:"name"`
	Make        string `json:"make"`
	Model       string `json:"model"`
	CurrentMode *int   `json:"current_mode"`
	Modes       []struct {
		Width       int  `json:"width"`
		Height      int  `json:"height"`
		RefreshRate int  `json:"refresh_rate"`
		IsPreferred bool `json:"is_preferred"`
	} `json:"modes"`
	Logical *struct {
		X      int     `json:"x"`
		Y      int     `json:"y"`
		Width  int     `json:"width"`
		Height int     `json:"height"`
		Scale  float64 `json:"scale"`
	} `json:"logical"`
}

// detectDisplays asks niri for the connected outputs. Best-effort: an unknown
// compositor returns an empty list and the caller falls back to config values.
func detectDisplays(ctx context.Context) []Display {
	out, err := runCmd(ctx, 4*time.Second, "niri", "msg", "--json", "outputs")
	if err != nil {
		return nil
	}
	// `niri msg --json outputs` prints a JSON object keyed by connector name.
	var byName map[string]niriOutput
	if err := json.Unmarshal(out, &byName); err != nil {
		return nil
	}
	var displays []Display
	for name, o := range byName {
		if o.CurrentMode == nil || *o.CurrentMode >= len(o.Modes) {
			continue
		}
		m := o.Modes[*o.CurrentMode]
		d := Display{Connector: name, Width: m.Width, Height: m.Height, Scale: 1}
		if o.Logical != nil {
			d.Scale = o.Logical.Scale
			d.X, d.Y = o.Logical.X, o.Logical.Y
		}
		displays = append(displays, d)
	}
	sort.Slice(displays, func(i, j int) bool {
		if displays[i].X != displays[j].X {
			return displays[i].X < displays[j].X
		}
		return displays[i].Connector < displays[j].Connector
	})
	return displays
}

// maxDisplayWidth is the widest current mode across all connected outputs.
func maxDisplayWidth(displays []Display) int {
	max := 0
	for _, d := range displays {
		if d.Width > max {
			max = d.Width
		}
	}
	return max
}

// detectConnectors lists the currently connected output names.
func detectConnectors(ctx context.Context) []string {
	displays := detectDisplays(ctx)
	out := make([]string, 0, len(displays))
	for _, d := range displays {
		if d.Connector != "" {
			out = append(out, d.Connector)
		}
	}
	return out
}

// ---------------------------------------------------------------- directory resolution

// resolveDirectory mirrors Noctalia's own precedence: an explicit config value,
// then the shell's [wallpaper] directory (theme-mode aware), then XDG Pictures.
func resolveDirectory(ctx context.Context, cfg Config) string {
	if d := expandUser(cfg.Directory); d != "" {
		return d
	}
	dirs := noctaliaWallpaperDirs(ctx, cfg)
	if len(dirs) > 0 {
		mode := noctaliaThemeMode(ctx, cfg)
		if v := dirs[dirKeyForMode(mode)]; v != "" {
			return expandUser(v)
		}
		if v := dirs["directory"]; v != "" {
			return expandUser(v)
		}
	}
	if v := xdgPicturesDir(); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "Wallpapers"
	}
	return filepath.Join(home, "Pictures", "Wallpapers")
}

func dirKeyForMode(mode string) string {
	switch mode {
	case "light":
		return "directory_light"
	case "dark":
		return "directory_dark"
	default:
		return "directory"
	}
}

// noctaliaWallpaperDirs returns the [wallpaper] directory keys exactly as the
// shell resolves them. Noctalia persists GUI/IPC changes into settings.toml in
// the STATE dir — that override layer must win; the config dir only holds
// hand-written drop-ins. Reading only the config dir is the classic way to end
// up watching the wrong folder.
func noctaliaWallpaperDirs(ctx context.Context, cfg Config) map[string]string {
	out := map[string]string{}
	if merged := noctaliaMergedConfig(ctx, cfg); merged != "" {
		parseWallpaperDirs(merged, out)
		if out["directory"] != "" || out["directory_light"] != "" || out["directory_dark"] != "" {
			return out
		}
	}
	for _, path := range noctaliaConfigFiles() {
		if data, err := os.ReadFile(path); err == nil {
			parseWallpaperDirs(string(data), out)
		}
	}
	return out
}

// noctaliaConfigFiles lists the TOML layers in load order: every *.toml in the
// config dir (alphabetical), then the state-dir settings.toml overrides last.
func noctaliaConfigFiles() []string {
	var files []string
	dir := noctaliaConfigDir()
	if entries, err := os.ReadDir(dir); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".toml") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			files = append(files, filepath.Join(dir, n))
		}
	}
	return append(files, filepath.Join(noctaliaStateDir(), "settings.toml"))
}

func noctaliaConfigDir() string {
	if v := strings.TrimSpace(os.Getenv("NOCTALIA_CONFIG_HOME")); v != "" {
		return filepath.Join(v, "noctalia")
	}
	return filepath.Join(configHome(), "noctalia")
}

func noctaliaStateDir() string {
	if v := strings.TrimSpace(os.Getenv("NOCTALIA_STATE_HOME")); v != "" {
		return filepath.Join(v, "noctalia")
	}
	return filepath.Join(stateHome(), "noctalia")
}

// noctaliaMergedConfig runs `noctalia config export` and returns its TOML.
func noctaliaMergedConfig(ctx context.Context, cfg Config) string {
	bin := noctaliaBin(cfg)
	if !hasBinary(bin) {
		return ""
	}
	out, err := runCmd(ctx, 4*time.Second, bin, "config", "export")
	if err != nil {
		return ""
	}
	return string(out)
}

// parseWallpaperDirs is a tiny TOML reader scoped to the [wallpaper] section's
// directory keys. Later files override earlier ones, matching Noctalia.
func parseWallpaperDirs(toml string, out map[string]string) {
	section := ""
	for _, raw := range strings.Split(toml, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[]")
			continue
		}
		if section != "wallpaper" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key = strings.TrimSpace(key); key {
		case "directory", "directory_light", "directory_dark":
			if v := unquoteTOML(strings.TrimSpace(value)); v != "" {
				out[key] = v
			}
		}
	}
}

func unquoteTOML(v string) string {
	if i := strings.IndexByte(v, '#'); i > 0 && !strings.Contains(v[:i], "\"") {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		q := v[0]
		v = v[1:]
		if j := strings.LastIndexByte(v, q); j >= 0 {
			v = v[:j]
		}
	}
	return strings.TrimSpace(v)
}

func xdgPicturesDir() string {
	if v := strings.TrimSpace(os.Getenv("XDG_PICTURES_DIR")); v != "" {
		return expandUser(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(configHome(), "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "XDG_PICTURES_DIR") {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v := unquoteTOML(strings.TrimSpace(value))
		return strings.ReplaceAll(v, "$HOME", home)
	}
	return ""
}

// ---------------------------------------------------------------- theme mode

// noctaliaThemeMode asks the running shell for its resolved mode so the right
// directory_light / directory_dark bucket is used. Empty when unavailable.
func noctaliaThemeMode(ctx context.Context, cfg Config) string {
	bin := noctaliaBin(cfg)
	if !hasBinary(bin) {
		return ""
	}
	out, err := runCmd(ctx, 3*time.Second, bin, "msg", "theme-mode-get")
	if err != nil {
		return ""
	}
	s := strings.ToLower(string(out))
	switch {
	case strings.Contains(s, "light"):
		return "light"
	case strings.Contains(s, "dark"):
		return "dark"
	default:
		return ""
	}
}

// noctaliaBin returns the shell binary to talk to.
func noctaliaBin(cfg Config) string {
	if b := strings.TrimSpace(cfg.NoctaliaBin); b != "" {
		return b
	}
	return "noctalia"
}

// noctaliaAvailable reports whether the shell binary exists and the shell
// answers IPC (a binary on PATH with no running shell is not enough).
func noctaliaAvailable(ctx context.Context, cfg Config) bool {
	bin := noctaliaBin(cfg)
	if !hasBinary(bin) {
		return false
	}
	_, err := runCmd(ctx, 3*time.Second, bin, "msg", "wallpaper-get")
	return err == nil
}
