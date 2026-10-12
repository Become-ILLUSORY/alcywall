package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// cmdProbe checks the API: which hosts answer, how fast, and what each pool
// currently returns. Everything is best-effort and read-only.
func cmdProbe(ctx context.Context, args []string) error {
	o := newOpts()
	var allPools bool
	fs := newFlagSet("probe", o, func(fs *flag.FlagSet) {
		fs.BoolVar(&allPools, "all", false, "probe every pool, not just the configured one")
	})
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	log := newLogger(o.verbose)
	cfg, err := buildConfig(o)
	if err != nil {
		return err
	}

	hc := &http.Client{Timeout: 15 * time.Second}

	// 1) Reachability + latency of the JSON endpoint.
	fmt.Printf("API          : %s\n", cfg.BaseURL)
	started := time.Now()
	client := NewClient(cfg, hc, log)
	_, err = client.Search(ctx, cfg.Category, 1)
	latency := time.Since(started)
	if err != nil {
		fmt.Printf("status       : UNREACHABLE (%v)\n", err)
	} else {
		fmt.Printf("status       : ok (%s round trip)\n", latency.Round(time.Millisecond))
	}

	// 2) What the configured pool returns right now, and at which size.
	if err == nil {
		pics, serr := client.Search(ctx, cfg.Category, 3)
		if serr == nil && len(pics) > 0 {
			p := pics[0]
			fmt.Printf("sample       : %s\n", p.Link)
			if dims := fetchDimensions(ctx, hc, sizedLink(p.Link, 0, 0)); dims != "" {
				fmt.Printf("original     : %s\n", dims)
			}
			if dims := fetchDimensions(ctx, hc, sizedLink(p.Link, 2560, 0)); dims != "" {
				fmt.Printf("at ?w=2560   : %s\n", dims)
			}
		}
	}

	// 3) The pools themselves.
	list := []Pool{}
	if allPools {
		list = wallpaperPools()
	} else {
		if p, ok := poolByKey(cfg.Category); ok {
			list = []Pool{p}
		}
	}
	fmt.Printf("\n%-8s %-6s %-8s %s\n", "POOL", "KIND", "STATUS", "NOTE")
	for _, p := range list {
		status, note := probePool(ctx, client, p)
		fmt.Printf("%-8s %-6s %-8s %s\n", p.Key, p.Kind, status, note)
	}

	if !allPools {
		fmt.Printf("\nrun `alcywall probe -all` to test every pool\n")
	}
	return nil
}

// probePool fetches one picture from the pool and reports whether it is usable.
func probePool(ctx context.Context, client *Client, p Pool) (string, string) {
	if p.Kind == "video" {
		return "skip", "video pool (not a wallpaper)"
	}
	pics, err := client.Search(ctx, p.Key, 1)
	if err != nil {
		return "fail", truncate(err.Error(), 60)
	}
	if len(pics) == 0 {
		return "empty", ""
	}
	link := pics[0].Link
	if IsVideoLink(link) {
		return "video", "returned a video file"
	}
	return "ok", p.Label
}

// fetchDimensions downloads just enough of an image to read its pixel size.
func fetchDimensions(ctx context.Context, hc *http.Client, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Range", "bytes=0-65535")
	resp, err := hc.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	buf := make([]byte, 64*1024)
	n, _ := resp.Body.Read(buf)
	w, h := imageSize(buf[:n])
	if w == 0 || h == 0 {
		return ""
	}
	return fmt.Sprintf("%d × %d", w, h)
}

// imageSize reads the pixel dimensions of a WebP, PNG, JPEG or GIF header.
// Returns (0, 0) when the format is not recognised.
func imageSize(b []byte) (int, int) {
	if len(b) < 30 {
		return 0, 0
	}
	// PNG: \x89PNG\r\n\x1a\n then IHDR with width/height at offset 16.
	if b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' {
		return be32(b[16:20]), be32(b[20:24])
	}
	// GIF: "GIF87a"/"GIF89a", little-endian 16-bit at 6 and 8.
	if string(b[:3]) == "GIF" {
		return le16(b[6:8]), le16(b[8:10])
	}
	// WebP: "RIFF"…"WEBP" then a chunk.
	if string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return webpSize(b)
	}
	// JPEG: walk the markers until a SOFn frame header.
	if b[0] == 0xFF && b[1] == 0xD8 {
		return jpegSize(b)
	}
	return 0, 0
}

func be32(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
}

func le16(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	return int(b[0]) | int(b[1])<<8
}

func webpSize(b []byte) (int, int) {
	if len(b) < 30 {
		return 0, 0
	}
	chunk := string(b[12:16])
	switch chunk {
	case "VP8 ":
		// Lossy: 3-byte frame tag, then the 0x9d012a sync code.
		i := 20
		if len(b) < i+10 {
			return 0, 0
		}
		if b[i] != 0x9d || b[i+1] != 0x01 || b[i+2] != 0x2a {
			return 0, 0
		}
		w := int(b[i+3]) | int(b[i+4])<<8
		h := int(b[i+5]) | int(b[i+6])<<8
		return w & 0x3fff, h & 0x3fff
	case "VP8L":
		// Lossless: signature 0x2f then 14-bit width-1 / height-1.
		if len(b) < 25 || b[20] != 0x2f {
			return 0, 0
		}
		bits := uint32(b[21]) | uint32(b[22])<<8 | uint32(b[23])<<16 | uint32(b[24])<<24
		w := int(bits&0x3fff) + 1
		h := int((bits>>14)&0x3fff) + 1
		return w, h
	case "VP8X":
		// Extended: canvas size stored as 24-bit minus one at offset 24/27.
		if len(b) < 30 {
			return 0, 0
		}
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1
	}
	return 0, 0
}

func jpegSize(b []byte) (int, int) {
	i := 2
	for i+9 < len(b) {
		if b[i] != 0xFF {
			i++
			continue
		}
		marker := b[i+1]
		// SOF0..SOF3, SOF5..SOF7, SOF9..SOF11, SOF13..SOF15
		if (marker >= 0xC0 && marker <= 0xC3) ||
			(marker >= 0xC5 && marker <= 0xC7) ||
			(marker >= 0xC9 && marker <= 0xCB) ||
			(marker >= 0xCD && marker <= 0xCF) {
			h := int(b[i+5])<<8 | int(b[i+6])
			w := int(b[i+7])<<8 | int(b[i+8])
			return w, h
		}
		segLen := int(b[i+2])<<8 | int(b[i+3])
		if segLen < 2 {
			return 0, 0
		}
		i += 2 + segLen
	}
	return 0, 0
}

// jsonPretty is a tiny helper used by -json outputs in other commands.
func jsonPretty(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

var _ = strings.TrimSpace
