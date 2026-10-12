package main

import (
	"context"
	"math/rand"
	"time"
)

// cmdWatch keeps rotating the wallpaper in the foreground. Useful when you do
// not want systemd: run it from your compositor's autostart.
func cmdWatch(ctx context.Context, args []string) error {
	o := newOpts()
	fs := newFlagSet("watch", o, nil)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	log := newLogger(o.verbose)
	cfg, err := buildConfig(o)
	if err != nil {
		return err
	}
	interval := o.interval
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	jitter := o.jitter
	if jitter < 0 {
		jitter = 0
	}

	log.Infof("rotating every %s (jitter up to %s), Ctrl-C to stop", interval, jitter)

	runOnce := func() {
		if _, err := runPipeline(ctx, cfg, o, log, 1); err != nil {
			log.Warnf("%v", err)
		}
	}

	// First rotation right away, then on the timer.
	runOnce()

	timer := time.NewTimer(nextDelay(interval, jitter))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Infof("stopped")
			return nil
		case <-timer.C:
			runOnce()
			timer.Reset(nextDelay(interval, jitter))
		}
	}
}

func nextDelay(interval, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return interval
	}
	return interval + time.Duration(rand.Int63n(int64(jitter)))
}
