package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// State is the small persistent bookkeeping file: which picture ids were shown
// recently (so the random pick does not repeat), and the last few wallpapers.
type State struct {
	Recent  []RecentEntry  `json:"recent"`
	History []HistoryEntry `json:"history"`
	// LastEndpoint records the API origin that answered last, so a mirror can
	// become the default after a successful probe.
	LastEndpoint string `json:"last_endpoint,omitempty"`
}

type RecentEntry struct {
	ID      string    `json:"id"`
	ShownAt time.Time `json:"shown_at"`
}

type HistoryEntry struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	URL       string    `json:"url"`
	Category  string    `json:"category,omitempty"`
	AppliedAt time.Time `json:"applied_at"`
}

func loadState() State {
	var st State
	data, err := os.ReadFile(statePath())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func (s *State) save() error {
	path := statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
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

// recentSet is the set of picture ids shown recently, as a plain map for O(1)
// lookups while iterating candidates.
func (s State) recentSet() map[string]bool {
	out := make(map[string]bool, len(s.Recent))
	for _, r := range s.Recent {
		out[r.ID] = true
	}
	return out
}

// remember records an id, keeping the window bounded.
func (s *State) remember(id string, window int) {
	if window <= 0 {
		window = 400
	}
	s.Recent = append(s.Recent, RecentEntry{ID: id, ShownAt: time.Now()})
	if len(s.Recent) > window {
		s.Recent = s.Recent[len(s.Recent)-window:]
	}
}

func trimHistory(h []HistoryEntry, max int) []HistoryEntry {
	if len(h) <= max {
		return h
	}
	return h[:max]
}

// ---------------------------------------------------------------- pruning

// prune keeps the newest `keep` files in dir and returns how many it removed.
// Only files this tool created (the "alcy-" prefix) are considered, so a
// hand-curated wallpaper folder is never touched.
func prune(dir string, keep int, log *logger) int {
	if keep <= 0 {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	type fileInfo struct {
		path string
		mod  time.Time
	}
	var ours []fileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "alcy-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		ours = append(ours, fileInfo{path: filepath.Join(dir, name), mod: info.ModTime()})
	}
	if len(ours) <= keep {
		return 0
	}
	sort.Slice(ours, func(i, j int) bool { return ours[i].mod.After(ours[j].mod) })
	removed := 0
	for _, f := range ours[keep:] {
		if err := os.Remove(f.path); err == nil {
			removed++
		} else {
			log.Debugf("prune %s: %v", f.path, err)
		}
	}
	if removed > 0 {
		log.Debugf("pruned %d old wallpaper(s)", removed)
	}
	return removed
}

// ---------------------------------------------------------------- history cmd

func cmdHistory(ctx context.Context, args []string) error {
	o := newOpts()
	fs := newFlagSet("history", o, nil)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	st := loadState()
	if len(st.History) == 0 {
		fmt.Println("no history yet")
		return nil
	}
	for _, h := range st.History {
		exists := "missing"
		if fileExists(h.Path) {
			exists = "on disk"
		}
		fmt.Printf("%s  %-8s %-10s %s (%s)\n",
			h.AppliedAt.Format("2006-01-02 15:04"), h.Category, h.ID, h.Path, exists)
	}
	return nil
}

// ---------------------------------------------------------------- config cmd

func cmdConfig(ctx context.Context, args []string) error {
	o := newOpts()
	var initFile bool
	fs := newFlagSet("config", o, func(fs *flag.FlagSet) {
		fs.BoolVar(&initFile, "init", false, "write the default config file if missing")
	})
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	path := o.cfgFile
	if path == "" {
		path = configPath()
	}

	if initFile {
		if fileExists(path) {
			fmt.Printf("%s already exists, leaving it alone\n", path)
		} else {
			cfg := defaultConfig()
			if err := cfg.save(path); err != nil {
				return err
			}
			fmt.Printf("wrote %s\n", path)
		}
	}

	cfg, err := buildConfig(o)
	if err != nil {
		return err
	}
	dir := resolveDirectory(ctx, cfg)

	fmt.Printf("config file : %s\n", path)
	fmt.Printf("state file  : %s\n", statePath())
	fmt.Printf("base url    : %s\n", cfg.BaseURL)
	pool, _ := poolByKey(cfg.Category)
	fmt.Printf("category    : %s (%s, %s)\n", cfg.Category, pool.Label, pool.Kind)
	fmt.Printf("width       : %s\n", widthDescription(cfg))
	fmt.Printf("directory   : %s\n", dir)
	fmt.Printf("keep        : %d\n", cfg.Keep)
	fmt.Printf("notify      : %v\n", cfg.Notify)
	fmt.Printf("backend     : %s\n", cfg.ApplyBackend)
	fmt.Printf("niri sync   : %v\n", cfg.NiriSyncBackground && cfg.UseNiri)
	fmt.Printf("retries     : %d\n", cfg.Retries)
	fmt.Printf("timeout     : %ds\n", cfg.TimeoutSeconds)
	return nil
}

func widthDescription(cfg Config) string {
	switch {
	case cfg.Width > 0:
		return fmt.Sprintf("%d px (fixed)", cfg.Width)
	case cfg.FitToDisplay:
		return "auto (widest connected monitor, capped at 4096)"
	default:
		return "original (no ?w= parameter)"
	}
}

// ---------------------------------------------------------------- state helpers for doctor

func stateFilePath() (string, error) {
	p := statePath()
	if p == "" {
		return "", errors.New("cannot resolve the state path")
	}
	return p, nil
}
