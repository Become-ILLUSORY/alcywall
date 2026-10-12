package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- pools

// Pool describes one category of the 栗次元 API.
//
// The bare endpoints (https://t.alcy.cc/ycy) answer with a 302 to a random
// image; the JSON endpoints (https://t.alcy.cc/json?ycy) answer with
// {"code":200,"category":"ycy","count":1,"data":{"id":..,"link":..}} and are
// what we use for the actual pick, because the link can be re-requested with
// ?w= / ?h= scaling parameters and gives us the file id for dedup.
type Pool struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // image | video
	// Auto is true for the adaptive pools (/ycy /moez /ysz /mikuz), which pick a
	// horizontal or vertical image based on the requesting User-Agent.
	Auto bool `json:"auto"`
}

var pools = []Pool{
	{Key: "ycy", Label: "二次元自适应（横竖自动）", Kind: "image", Auto: true},
	{Key: "moez", Label: "萌版自适应", Kind: "image", Auto: true},
	{Key: "ysz", Label: "原神自适应", Kind: "image", Auto: true},
	{Key: "mikuz", Label: "初音未来自适应", Kind: "image", Auto: true},
	{Key: "pc", Label: "PC 横图", Kind: "image"},
	{Key: "fj", Label: "风景横图", Kind: "image"},
	{Key: "moe", Label: "萌版横图", Kind: "image"},
	{Key: "mp", Label: "手机竖图", Kind: "image"},
	{Key: "miku", Label: "初音未来横图", Kind: "image"},
	{Key: "ys", Label: "原神横图", Kind: "image"},
	{Key: "ysmp", Label: "原神竖图", Kind: "image"},
	{Key: "moemp", Label: "萌版竖图", Kind: "image"},
	{Key: "mikump", Label: "初音未来竖图", Kind: "image"},
	{Key: "tx", Label: "头像方图", Kind: "image"},
	{Key: "bd", Label: "白底素材", Kind: "image"},
	{Key: "xhl", Label: "小狐狸", Kind: "image"},
	{Key: "lai", Label: "七濑胡桃", Kind: "image"},
	{Key: "acg", Label: "ACG 动图（mp4，不能做壁纸）", Kind: "video"},
}

func poolByKey(key string) (Pool, bool) {
	for _, p := range pools {
		if strings.EqualFold(p.Key, key) {
			return p, true
		}
	}
	return Pool{}, false
}

// wallpaperPools is the subset that can actually be used as a wallpaper.
func wallpaperPools() []Pool {
	out := make([]Pool, 0, len(pools))
	for _, p := range pools {
		if p.Kind == "image" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------- config

// Config is the user-facing file. Only the fields below appear in
// ~/.config/alcywall/config.json; runtime knobs (timeouts, size guards) live in
// defaultConfig() and can be overridden by flags or ALCYWALL_* env vars, so the
// config file stays short and readable.
type Config struct {
	// BaseURL is the API origin. Override it if you self-host a mirror of the
	// picture service; both the JSON and the bare endpoints hang off it.
	BaseURL string `json:"base_url,omitempty"`

	// Category is the default pool key (see `alcywall probe`).
	Category string `json:"category,omitempty"`

	// Width/Height are passed to the API as ?w= / ?h=. The service scales the
	// image down to the requested width; it never upscales and never rejects
	// oversized requests, so a small original simply comes back unchanged.
	// 0 disables the parameter (full-size original). Server cap: 4096.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`

	Directory string `json:"directory,omitempty"` // empty = auto-detect from Noctalia
	Keep      int    `json:"keep,omitempty"`      // downloaded files to retain
	Notify    bool   `json:"notify"`              // desktop notification on change

	// NiriSyncBackground writes the sampled average colour of the new wallpaper
	// into niri's per-output background-color, so the compositor background
	// matches the shell while an image is still loading.
	NiriSyncBackground bool `json:"niri_sync_background"`

	// ApplyBackend: auto | noctalia | niri.
	ApplyBackend string `json:"apply_backend,omitempty"`

	// ---- runtime-only (flags / env / defaults), not part of the config file ----
	Apply          bool   `json:"-"`
	NoctaliaBin    string `json:"-"`
	NotifyApp      string `json:"-"`
	Retries        int    `json:"-"`
	TimeoutSeconds int    `json:"-"`
	MinFileBytes   int64  `json:"-"`
	MaxFileBytes   int64  `json:"-"`
	RecentWindow   int    `json:"-"`
	FitToDisplay   bool   `json:"-"`
	UseNiri        bool   `json:"-"`
	Connector      string `json:"-"`
}

// maxAPISize is the largest ?w= the picture service accepts; anything above it
// answers 404 page not found instead of an image.
const maxAPISize = 4096

func defaultConfig() Config {
	return Config{
		BaseURL:            "https://t.alcy.cc",
		Category:           "ycy",
		Width:              0, // keep the original unless asked otherwise
		Directory:          "",
		Keep:               40,
		Notify:             true,
		NiriSyncBackground: true,
		ApplyBackend:       "auto",

		Apply:          true,
		NoctaliaBin:    "noctalia",
		NotifyApp:      "alcywall",
		Retries:        5,
		TimeoutSeconds: 45,
		MinFileBytes:   16 * 1024,
		MaxFileBytes:   64 * 1024 * 1024,
		RecentWindow:   400,
		FitToDisplay:   true,
		UseNiri:        true,
	}
}

// ---------------------------------------------------------------- paths

func xdgDir(envVar, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" && filepath.IsAbs(v) {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, fallback)
}

func configHome() string { return xdgDir("XDG_CONFIG_HOME", ".config") }
func stateHome() string  { return xdgDir("XDG_STATE_HOME", ".local/state") }
func cacheHome() string  { return xdgDir("XDG_CACHE_HOME", ".cache") }

func configPath() string {
	return filepath.Join(configHome(), "alcywall", "config.json")
}

func statePath() string {
	return filepath.Join(stateHome(), "alcywall", "state.json")
}

func expandUser(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return os.ExpandEnv(p)
}

// ---------------------------------------------------------------- load / save

func loadConfigFile(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	// Non-strict keeps forward-compatible upgrades from breaking older builds.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	applyDefaults(&cfg)
	return cfg, nil
}

// applyDefaults repairs fields a partial config file may have zeroed out.
func applyDefaults(cfg *Config) {
	def := defaultConfig()
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = def.BaseURL
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if strings.TrimSpace(cfg.Category) == "" {
		cfg.Category = def.Category
	}
	if cfg.Keep <= 0 {
		cfg.Keep = def.Keep
	}
	if cfg.Retries <= 0 {
		cfg.Retries = def.Retries
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = def.TimeoutSeconds
	}
	if cfg.MinFileBytes <= 0 {
		cfg.MinFileBytes = def.MinFileBytes
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = def.MaxFileBytes
	}
	if cfg.RecentWindow <= 0 {
		cfg.RecentWindow = def.RecentWindow
	}
	if strings.TrimSpace(cfg.NoctaliaBin) == "" {
		cfg.NoctaliaBin = def.NoctaliaBin
	}
	if strings.TrimSpace(cfg.NotifyApp) == "" {
		cfg.NotifyApp = def.NotifyApp
	}
	if strings.TrimSpace(cfg.ApplyBackend) == "" {
		cfg.ApplyBackend = def.ApplyBackend
	}
	if cfg.Width < 0 {
		cfg.Width = 0
	}
	if cfg.Width > maxAPISize {
		cfg.Width = maxAPISize
	}
	if cfg.Height < 0 {
		cfg.Height = 0
	}
	if cfg.Height > maxAPISize {
		cfg.Height = maxAPISize
	}
}

func (c Config) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// applyEnv layers ALCYWALL_* environment variables on top of the file config.
func (c *Config) applyEnv() {
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_BASE_URL")); v != "" {
		c.BaseURL = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_CATEGORY")); v != "" {
		c.Category = v
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_DIR")); v != "" {
		c.Directory = v
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_WIDTH")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Width = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_HEIGHT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Height = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_KEEP")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Keep = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_TIMEOUT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.TimeoutSeconds = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_RETRIES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Retries = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_NO_APPLY")); v == "1" || strings.EqualFold(v, "true") {
		c.Apply = false
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_NO_NIRI")); v == "1" || strings.EqualFold(v, "true") {
		c.UseNiri = false
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_CONNECTOR")); v != "" {
		c.Connector = v
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_BACKEND")); v != "" {
		c.ApplyBackend = v
	}
	if v := strings.TrimSpace(os.Getenv("NOCTALIA_BIN")); v != "" {
		c.NoctaliaBin = v
	}
	if v := strings.TrimSpace(os.Getenv("ALCYWALL_NOTIFY")); v == "0" || strings.EqualFold(v, "false") {
		c.Notify = false
	}
}

// buildConfig resolves config file + env + flags into the final configuration.
func buildConfig(o *opts) (Config, error) {
	path := o.cfgFile
	if path == "" {
		path = configPath()
	}
	cfg, err := loadConfigFile(path)
	if err != nil {
		return cfg, err
	}
	cfg.applyEnv()

	if o.category != "" {
		cfg.Category = o.category
	}
	if o.directory != "" {
		cfg.Directory = o.directory
	}
	if o.connector != "" {
		cfg.Connector = o.connector
	}
	if o.width > 0 {
		cfg.Width = o.width
	}
	if o.height > 0 {
		cfg.Height = o.height
	}
	if o.keep > 0 {
		cfg.Keep = o.keep
	}
	if o.retries > 0 {
		cfg.Retries = o.retries
	}
	if o.timeout > 0 {
		cfg.TimeoutSeconds = o.timeout
	}
	if o.silent || !o.notify {
		cfg.Notify = false
	}
	if o.noApply {
		cfg.Apply = false
	}
	if o.noNiri {
		cfg.UseNiri = false
	}
	if o.noFit {
		cfg.FitToDisplay = false
	}
	if o.backend != "" {
		cfg.ApplyBackend = o.backend
	}

	if _, ok := poolByKey(cfg.Category); !ok {
		return cfg, fmt.Errorf("unknown category %q (run `alcywall probe` for the list)", cfg.Category)
	}
	applyDefaults(&cfg)
	return cfg, nil
}
