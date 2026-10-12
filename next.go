package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Result is one wallpaper fetch, printed as text or JSON.
type Result struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	ImageURL  string `json:"image_url"`
	LocalPath string `json:"local_path"`
	Width     int    `json:"requested_width,omitempty"`
	Bytes     int64  `json:"bytes"`
	Source    string `json:"source"`
	Endpoint  string `json:"endpoint"`
	Applied   bool   `json:"applied"`
	Backend   string `json:"backend,omitempty"`
	Removed   int    `json:"pruned"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

func cmdNext(ctx context.Context, args []string) error {
	o := newOpts()
	fs := newFlagSet("next", o, func(f *flag.FlagSet) {
		f.IntVar(&o.count, "count", 1, "fetch several wallpapers at once (only the last is applied)")
	})
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	log := newLogger(o.verbose)
	cfg, err := buildConfig(o)
	if err != nil {
		return err
	}
	n := o.count
	if n < 1 {
		n = 1
	}

	results, err := runPipeline(ctx, cfg, o, log, n)
	for _, r := range results {
		printResult(r, o.jsonOut)
	}
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return errors.New("no wallpaper fetched")
	}
	return nil
}

// runPipeline is shared by next and watch.
func runPipeline(ctx context.Context, cfg Config, o *opts, log *logger, count int) ([]Result, error) {
	dir := resolveDirectory(ctx, cfg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	// Pick the request width: an explicit config/flag value wins, otherwise ask
	// the API for exactly what the widest monitor needs — the service scales
	// down but never up, so oversized requests are free and small originals
	// simply come back untouched.
	width := cfg.Width
	if width <= 0 && cfg.FitToDisplay && !o.noFit {
		if displays := detectDisplays(ctx); len(displays) > 0 {
			width = maxDisplayWidth(displays)
			if width > maxAPISize {
				width = maxAPISize
			}
			log.Debugf("fit to display: requesting %dpx wide", width)
		}
	}

	hc := &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	client := NewClient(cfg, hc, log)
	st := loadState()

	var results []Result
	var lastErr error
	for i := 0; i < count; i++ {
		res, err := fetchOnce(ctx, cfg, o, log, dir, width, &st, client, hc)
		if err != nil {
			lastErr = err
			break
		}
		if res == nil {
			continue
		}
		results = append(results, *res)
	}
	return results, lastErr
}

func fetchOnce(ctx context.Context, cfg Config, o *opts, log *logger, dir string, width int, st *State, client *Client, hc *http.Client) (*Result, error) {
	started := time.Now()
	recent := st.recentSet()
	pool, _ := poolByKey(cfg.Category)
	var lastApplyErr error

	for attempt := 0; attempt < cfg.Retries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Ask for a batch so a rejected candidate (duplicate, video, dead link)
		// does not burn another round trip.
		want := 4
		if attempt > 0 {
			want = 6
		}
		pics, err := client.Search(ctx, pool.Key, want)
		if err != nil {
			log.Debugf("attempt %d: %v", attempt+1, err)
			if wait, ok := rateLimitWait(err); ok {
				log.Warnf("rate limited, waiting %s", wait)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
			}
			continue
		}

		rand.Shuffle(len(pics), func(i, j int) { pics[i], pics[j] = pics[j], pics[i] })

		for _, p := range pics {
			if !o.force && recent[p.ID] {
				log.Debugf("skip %s (shown recently)", p.ID)
				continue
			}
			if IsVideoLink(p.Link) {
				log.Debugf("skip %s (video, not usable as a wallpaper)", p.ID)
				continue
			}

			link := sizedLink(p.Link, width, cfg.Height)
			if o.dryRun {
				return &Result{
					ID: p.ID, Category: pool.Key, ImageURL: link,
					Width: width, Source: "dry-run", Endpoint: cfg.BaseURL,
					ElapsedMS: time.Since(started).Milliseconds(),
				}, nil
			}

			name := sanitizeName(p.ID)
			if ext := extFromLink(p.Link); ext != "" && !strings.HasSuffix(strings.ToLower(name), ext) {
				name += ext
			}
			target := uniquePath(dir, name)

			bytes, err := downloadFile(ctx, hc, link, target, cfg.MinFileBytes, cfg.MaxFileBytes, log)
			if err != nil {
				log.Debugf("candidate %s failed: %v", p.ID, err)
				removeFile(target)
				recent[p.ID] = true
				continue
			}

			res := Result{
				ID: p.ID, Category: pool.Key, ImageURL: link, LocalPath: target,
				Width: width, Bytes: bytes, Source: "download", Endpoint: cfg.BaseURL,
			}

			if cfg.Apply {
				if err := applyWallpaper(ctx, cfg, target, log); err != nil {
					log.Warnf("%v", err)
					lastApplyErr = err
					recent[p.ID] = true
					continue
				}
				res.Applied = true
				res.Backend = cfg.ApplyBackend
			}

			res.Removed = prune(dir, cfg.Keep, log)
			st.remember(p.ID, cfg.RecentWindow)
			st.History = append([]HistoryEntry{{
				ID: p.ID, Path: target, URL: link, Category: pool.Key,
				AppliedAt: time.Now(),
			}}, trimHistory(st.History, 50)...)
			if err := st.save(); err != nil {
				log.Warnf("state not saved: %v", err)
			}
			res.ElapsedMS = time.Since(started).Milliseconds()

			title := "壁纸已切换"
			if !res.Applied {
				title = "壁纸已下载"
			}
			notify(ctx, cfg, log, title, wallpaperSummary(pool, res), "preferences-desktop-wallpaper")
			return &res, nil
		}
		log.Debugf("attempt %d exhausted all %d candidates", attempt+1, len(pics))
	}

	err := errors.New("no wallpaper could be fetched")
	if lastApplyErr != nil {
		err = fmt.Errorf("no wallpaper could be fetched (last apply error: %v)", lastApplyErr)
	}
	title, body := failureNotice(err)
	notify(ctx, cfg, log, title, body, "dialog-warning")
	return nil, err
}

// wallpaperSummary is the human-facing notification body.
func wallpaperSummary(pool Pool, r Result) string {
	parts := []string{}
	if pool.Label != "" {
		parts = append(parts, pool.Label)
	}
	if r.Width > 0 {
		parts = append(parts, fmt.Sprintf("%dpx", r.Width))
	}
	if r.Bytes > 0 {
		parts = append(parts, humanBytes(r.Bytes))
	}
	return strings.Join(parts, " · ")
}

// failureNotice maps a raw error to a short (title, body) for the failure popup.
func failureNotice(err error) (string, string) {
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "no such host"), strings.Contains(low, "dial"),
		strings.Contains(low, "timeout"), strings.Contains(low, "connection"),
		strings.Contains(low, "reset"), strings.Contains(low, "eof"):
		return "下载失败", "网络不可达，运行 alcywall probe 体检"
	case strings.Contains(low, "no wallpaper backend"):
		return "切换失败", "Noctalia 未运行且没有 swaybg，图片已保存"
	case strings.Contains(low, "empty result"), strings.Contains(low, "unknown category"):
		return "下载失败", "该分类没有返回图片"
	case strings.Contains(low, "404"):
		return "下载失败", "接口返回 404（宽度超过 4096？）"
	default:
		return "下载失败", "获取失败，运行 alcywall next -v 查看详情"
	}
}

func rateLimitWait(err error) (time.Duration, bool) {
	var ae *apiError
	if !errors.As(err, &ae) {
		return 0, false
	}
	if ae.Status == http.StatusTooManyRequests {
		return 20 * time.Second, true
	}
	return 0, false
}

// extFromLink returns the file extension of a link, including the dot.
func extFromLink(link string) string {
	if i := strings.IndexAny(link, "?#"); i >= 0 {
		link = link[:i]
	}
	ext := filepath.Ext(link)
	if len(ext) > 6 {
		return ""
	}
	return ext
}

func printResult(r Result, asJSON bool) {
	if asJSON {
		data, err := json.MarshalIndent(r, "", "  ")
		if err == nil {
			fmt.Println(string(data))
		}
		return
	}
	if r.Source == "dry-run" {
		line := fmt.Sprintf("dry-run %s · %s", r.Category, r.ID)
		if r.Width > 0 {
			line += fmt.Sprintf(" · %dpx", r.Width)
		}
		fmt.Println(line)
		fmt.Println(r.ImageURL)
		return
	}
	state := "saved"
	if r.Applied {
		state = "applied"
	}
	line := fmt.Sprintf("%s %s · %s · %s", state, r.Category, r.ID, humanBytes(r.Bytes))
	if r.Width > 0 {
		line += fmt.Sprintf(" · %dpx", r.Width)
	}
	line += " · " + r.LocalPath
	if r.Removed > 0 {
		line += fmt.Sprintf(" [-%d old]", r.Removed)
	}
	fmt.Println(line)
}
