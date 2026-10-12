package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cmdDoctor checks the environment end to end and prints what is missing.
func cmdDoctor(ctx context.Context, args []string) error {
	o := newOpts()
	fs := newFlagSet("doctor", o, nil)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	log := newLogger(o.verbose)
	cfg, err := buildConfig(o)
	if err != nil {
		return err
	}

	ok := func(cond bool) string {
		if cond {
			return "ok"
		}
		return "MISSING"
	}

	fmt.Println("alcywall doctor")
	fmt.Println(strings.Repeat("-", 60))

	// --- desktop session ---------------------------------------------------
	fmt.Printf("session      : WAYLAND_DISPLAY=%q NIRI_SOCKET=%q\n",
		os.Getenv("WAYLAND_DISPLAY"), os.Getenv("NIRI_SOCKET"))

	displays := detectDisplays(ctx)
	if len(displays) == 0 {
		fmt.Printf("niri outputs : not detected (niri not running, or `niri` not on PATH)\n")
	} else {
		for _, d := range displays {
			fmt.Printf("niri output  : %-10s %dx%d @%.2fx (logical %dx%d at %d,%d)\n",
				d.Connector, d.Width, d.Height, d.Scale,
				d.LogicalWidth(), d.LogicalHeight(), d.X, d.Y)
		}
	}

	// --- backends ----------------------------------------------------------
	bin := noctaliaBin(cfg)
	hasBin := hasBinary(bin)
	fmt.Printf("noctalia bin : %s %s\n", bin, ok(hasBin))
	fmt.Printf("noctalia ipc : %s\n", ok(noctaliaAvailable(ctx, cfg)))
	fmt.Printf("swaybg       : %s (fallback image backend)\n", ok(hasBinary("swaybg")))
	fmt.Printf("magick       : %s (colour sampling for the niri background sync)\n",
		ok(hasBinary("magick") || hasBinary("convert")))
	fmt.Printf("notify-send  : %s (fallback when Noctalia IPC is down)\n", ok(hasBinary("notify-send")))

	// --- config / paths ----------------------------------------------------
	fmt.Printf("config file  : %s\n", configPath())
	fmt.Printf("state file   : %s\n", statePath())
	niriCfg := niriConfigPath()
	if niriCfg != "" {
		fmt.Printf("niri config  : %s %s\n", niriCfg, ok(fileExists(niriCfg)))
	}

	dir := resolveDirectory(ctx, cfg)
	dirState := "ok"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dirState = fmt.Sprintf("UNWRITABLE (%v)", err)
	}
	fmt.Printf("wallpaper dir: %s %s\n", dir, dirState)

	// --- API ---------------------------------------------------------------
	hc := &http.Client{Timeout: 15 * time.Second}
	client := NewClient(cfg, hc, log)
	started := time.Now()
	pics, err := client.Search(ctx, cfg.Category, 1)
	latency := time.Since(started).Round(time.Millisecond)
	if err != nil {
		fmt.Printf("api          : UNREACHABLE (%v)\n", err)
	} else {
		fmt.Printf("api          : ok, %d picture(s) in %s\n", len(pics), latency)
		if len(pics) > 0 {
			fmt.Printf("sample       : %s\n", pics[0].Link)
			if dims := fetchDimensions(ctx, hc, sizedLink(pics[0].Link, 0, 0)); dims != "" {
				fmt.Printf("sample size  : %s (original)\n", dims)
			}
		}
	}

	// --- recommendation ----------------------------------------------------
	fmt.Println(strings.Repeat("-", 60))
	switch {
	case noctaliaAvailable(ctx, cfg):
		fmt.Println("verdict      : Noctalia is running — image wallpapers will work.")
	case hasBinary("swaybg"):
		fmt.Println("verdict      : Noctalia is not answering; swaybg will be used.")
	default:
		fmt.Println("verdict      : no image backend. Start Noctalia, or install swaybg.")
	}
	if !hasBinary("magick") && !hasBinary("convert") {
		fmt.Println("tip          : pacman -S imagemagick  → enables the niri background-colour sync")
	}
	return nil
}

// ---------------------------------------------------------------- state dump

// stateJSON is used by the JSON flag of doctor-like commands.
func stateJSON() (string, error) {
	st := loadState()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

var _ = filepath.Join
