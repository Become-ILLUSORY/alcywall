// Command alcywall — random anime wallpapers from 栗次元 API (t.alcy.cc)
// for the niri compositor + Noctalia shell on Arch Linux.
//
// Zero external dependencies: standard library only, so the binary
// cross-compiles anywhere with CGO_ENABLED=0.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// userAgent identifies us to the picture API. The adaptive pools (/ycy, /moez,
// /ysz, /mikuz) read the User-Agent to decide between a horizontal and a
// vertical image, so we present a desktop browser string: this tool runs on a
// desktop, and the desktop pools are the ones that fit a monitor.
var userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36 alcywall/" + version

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := realMain(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "alcywall: "+err.Error())
		os.Exit(1)
	}
}

func realMain(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			io.WriteString(os.Stdout, usage)
			return nil
		case "-v", "--version", "version":
			fmt.Printf("alcywall %s (commit %s, built %s)\n", version, commit, date)
			return nil
		}
	}

	cmd, rest := "next", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, rest = args[0], args[1:]
	}

	switch cmd {
	case "next", "run":
		return cmdNext(ctx, rest)
	case "watch":
		return cmdWatch(ctx, rest)
	case "probe":
		return cmdProbe(ctx, rest)
	case "config":
		return cmdConfig(ctx, rest)
	case "history":
		return cmdHistory(ctx, rest)
	case "doctor":
		return cmdDoctor(ctx, rest)
	case "install":
		return cmdInstall(ctx, rest)
	case "uninstall":
		return cmdUninstall(ctx, rest)
	default:
		io.WriteString(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// opts holds every command-line override. Empty/zero values mean "not set",
// so the config file keeps authority.
type opts struct {
	cfgFile   string
	category  string
	directory string
	connector string
	width     int
	height    int
	keep      int
	retries   int
	timeout   int
	count     int
	interval  time.Duration
	notify    bool
	silent    bool
	noApply   bool
	noNiri    bool
	noFit     bool
	dryRun    bool
	jsonOut   bool
	verbose   bool
	force     bool
	backend   string
	jitter    time.Duration
	onBoot    string
}

func newOpts() *opts {
	return &opts{
		notify:   true,
		count:    1,
		interval: 30 * time.Minute,
		jitter:   2 * time.Minute,
		onBoot:   "2min",
	}
}

func newFlagSet(name string, o *opts, extra func(*flag.FlagSet)) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.cfgFile, "config", "", "path to config file (default $XDG_CONFIG_HOME/alcywall/config.json)")
	fs.StringVar(&o.category, "category", "", "image pool: ycy|moez|ysz|mikuz|pc|fj|moe|mp|miku|ys|tx|bd|xhl|lai|ysmp|moemp|mikump")
	fs.StringVar(&o.directory, "dir", "", "directory to store wallpapers in")
	fs.StringVar(&o.connector, "connector", "", "niri output name, e.g. DP-1 (empty = all monitors)")
	fs.IntVar(&o.width, "w", 0, "scale the image down to this pixel width (0 = config value, max 4096)")
	fs.IntVar(&o.height, "h", 0, "optional height hint passed to the API (server treats it as a max)")
	fs.IntVar(&o.keep, "keep", 0, "how many downloaded wallpapers to retain (0 = config value)")
	fs.IntVar(&o.retries, "retries", 0, "candidate attempts per run (0 = config value)")
	fs.IntVar(&o.timeout, "timeout", 0, "per-request timeout in seconds (0 = config value)")
	fs.BoolVar(&o.notify, "notify", true, "send desktop notifications")
	fs.BoolVar(&o.silent, "silent", false, "shorthand for -notify=false")
	fs.BoolVar(&o.noApply, "no-apply", false, "download only, do not touch the wallpaper")
	fs.BoolVar(&o.noNiri, "no-niri", false, "skip the niri background-color sync")
	fs.BoolVar(&o.noFit, "no-fit", false, "do not ask the API for a display-sized image")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the chosen wallpaper without downloading")
	fs.StringVar(&o.backend, "backend", "", "force a backend: noctalia | niri | auto")
	fs.BoolVar(&o.jsonOut, "json", false, "machine-readable JSON output")
	fs.BoolVar(&o.verbose, "v", false, "verbose logging")
	fs.BoolVar(&o.force, "force", false, "ignore the recent-id dedup list")
	if extra != nil {
		extra(fs)
	}
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	return nil
}

const usage = `alcywall — random 二次元 wallpapers from 栗次元 (t.alcy.cc) for niri + Noctalia

Usage:
  alcywall [command] [flags]

Commands:
  next        fetch one random wallpaper, download it and apply it (default)
  watch       stay in foreground and rotate every -interval
  probe       check the API, pick the fastest reachable host, report the pools
  config      show the effective configuration (-init writes the default file)
  history     list recently applied wallpapers
  doctor      check binary, directories, Noctalia IPC and API reachability
  install     install a systemd user service + timer
  uninstall   remove them
  help        this text

Everyday flags:
  -category ycy      image pool (see "alcywall probe" for the full list)
  -dir path          wallpaper directory (default: Noctalia's, else ~/Pictures/Wallpapers)
  -w 2560            downscale to this pixel width (0 = keep original)
  -keep 40           downloaded files to retain
  -silent            no desktop notification
  -v                 verbose logging

Advanced flags:
  -connector DP-1    apply to one output only     -no-apply    download only
  -backend noctalia  force noctalia | niri | auto -no-niri     skip niri bg-color sync
  -no-fit            don't request a display-sized image
  -dry-run           resolve without downloading  -json        machine-readable output
  -force             ignore recent dedup          -config p    alternate config file

Examples:
  alcywall next
  alcywall next -category moez -w 2560
  alcywall watch -interval 15m
  alcywall install -interval 30m
  alcywall probe -v
`
