package main

import "testing"

func TestUpsertCreatesMissingOutputBlock(t *testing.T) {
	in := "input {\n\tkeyboard {\n\t}\n}\n"
	got, changed := upsertNiriBackgroundColor(in, "DP-1", "#112233")
	if !changed {
		t.Fatal("expected a change")
	}
	want := "input {\n\tkeyboard {\n\t}\n}\n\noutput \"DP-1\" {\n\tbackground-color \"#112233\"\n}"
	if got != want {
		t.Fatalf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestUpsertReplacesExistingLegacySetting(t *testing.T) {
	in := "output \"DP-1\" {\n\tmode \"2560x1440\"\n\tbackground-color \"#000000\"\n}\n"
	got, changed := upsertNiriBackgroundColor(in, "DP-1", "#AABBCC")
	if !changed {
		t.Fatal("expected a change")
	}
	if want := "output \"DP-1\" {\n\tmode \"2560x1440\"\n\tbackground-color \"#AABBCC\"\n}\n"; got != want {
		t.Fatalf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	in := "output \"DP-1\" {\n\tbackground-color \"#AABBCC\"\n}\n"
	got, changed := upsertNiriBackgroundColor(in, "DP-1", "#AABBCC")
	if changed {
		t.Fatal("expected no change when the colour already matches")
	}
	if got != in {
		t.Fatalf("content must be untouched, got:\n%s", got)
	}
}

func TestUpsertLeavesOtherOutputsAlone(t *testing.T) {
	in := "output \"DP-1\" {\n\tbackground-color \"#000000\"\n}\n\noutput \"HDMI-A-1\" {\n\tbackground-color \"#FFFFFF\"\n}\n"
	got, changed := upsertNiriBackgroundColor(in, "DP-1", "#123456")
	if !changed {
		t.Fatal("expected a change")
	}
	want := "output \"DP-1\" {\n\tbackground-color \"#123456\"\n}\n\noutput \"HDMI-A-1\" {\n\tbackground-color \"#FFFFFF\"\n}\n"
	if got != want {
		t.Fatalf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestUpsertHandlesNestedLayoutBlock(t *testing.T) {
	in := "output \"DP-1\" {\n\tlayout {\n\t\tbackground-color \"#000000\"\n\t}\n}\n"
	got, changed := upsertNiriBackgroundColor(in, "DP-1", "#445566")
	if !changed {
		t.Fatal("expected a change")
	}
	if want := "output \"DP-1\" {\n\tlayout {\n\t\tbackground-color \"#445566\"\n\t}\n}\n"; got != want {
		t.Fatalf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestUpsertInsertsIntoNestedLayoutBlock(t *testing.T) {
	in := "output \"DP-1\" {\n\tlayout {\n\t\tdefault-column-width {\n\t\t\tproportion 0.5\n\t\t}\n\t}\n}\n"
	got, changed := upsertNiriBackgroundColor(in, "DP-1", "#778899")
	if !changed {
		t.Fatal("expected a change")
	}
	if !contains(got, "#778899") {
		t.Fatalf("colour missing from result:\n%s", got)
	}
	// The new key must land inside the layout block, not at the top level.
	if !contains(got, "\t\tbackground-color \"#778899\"") {
		t.Fatalf("colour not indented inside layout:\n%s", got)
	}
}

func TestUpsertHandlesBracesOnOneLine(t *testing.T) {
	in := "output \"DP-1\" { background-color \"#000000\" }\n"
	got, changed := upsertNiriBackgroundColor(in, "DP-1", "#ABCDEF")
	if !changed {
		t.Fatal("expected a change")
	}
	if !contains(got, "#ABCDEF") {
		t.Fatalf("colour missing:\n%s", got)
	}
}

func TestUpsertSurvivesCommentsAndNestedBlocks(t *testing.T) {
	in := "// my niri config\ninput {\n\tkeyboard {\n\t\txkb {\n\t\t\tlayout \"us\"\n\t\t}\n\t}\n}\n\noutput \"DP-1\" {\n\t// pin this monitor\n\tmode \"2560x1440\"\n}\n"
	got, changed := upsertNiriBackgroundColor(in, "DP-1", "#0F0F0F")
	if !changed {
		t.Fatal("expected a change")
	}
	if !contains(got, "// pin this monitor") {
		t.Fatal("comment was dropped")
	}
	if !contains(got, "layout \"us\"") {
		t.Fatal("unrelated nested config was damaged")
	}
	if !contains(got, "background-color \"#0F0F0F\"") {
		t.Fatalf("colour missing:\n%s", got)
	}
}

func TestSizedLink(t *testing.T) {
	cases := []struct {
		link     string
		w, h     int
		expected string
	}{
		{"https://t.alcy.cc/pic/pc/1.webp", 0, 0, "https://t.alcy.cc/pic/pc/1.webp"},
		{"https://t.alcy.cc/pic/pc/1.webp", 2560, 0, "https://t.alcy.cc/pic/pc/1.webp?w=2560"},
		{"https://t.alcy.cc/pic/pc/1.webp", 9000, 0, "https://t.alcy.cc/pic/pc/1.webp?w=4096"},
		{"https://t.alcy.cc/pic/pc/1.webp?w=800", 2560, 0, "https://t.alcy.cc/pic/pc/1.webp?w=2560"},
	}
	for _, c := range cases {
		if got := sizedLink(c.link, c.w, c.h); got != c.expected {
			t.Errorf("sizedLink(%q, %d, %d) = %q, want %q", c.link, c.w, c.h, got, c.expected)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"123.webp":       "alcy-123.webp",
		"../../etc/pass": "alcy-_.._etc_pass",
		"":               "alcy-wallpaper",
		"a b c.webp":     "alcy-a_b_c.webp",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPictureIDFromLink(t *testing.T) {
	cases := map[string]string{
		"https://t.alcy.cc/pic/pc/123.webp":            "123.webp",
		"https://t.alcy.cc/pic/pc/123.webp?w=2560":     "123.webp",
		"/pic/mp/2026-01-13-abc.webp":                  "2026-01-13-abc.webp",
		"https://t.alcy.cc/pic/ys/PixPin_2025-09.webp": "PixPin_2025-09.webp",
	}
	for in, want := range cases {
		if got := pictureIDFromLink(in); got != want {
			t.Errorf("pictureIDFromLink(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsVideoLink(t *testing.T) {
	if !IsVideoLink("https://t.alcy.cc/pic/acg/777383.mp4") {
		t.Error("mp4 should be detected as video")
	}
	if IsVideoLink("https://t.alcy.cc/pic/pc/123.webp") {
		t.Error("webp is not a video")
	}
}

func TestImageSizeWebP(t *testing.T) {
	// Minimal VP8X header: RIFF....WEBPVP8X + 10-byte chunk + 24-bit sizes.
	b := make([]byte, 40)
	copy(b[0:], "RIFF")
	copy(b[8:], "WEBP")
	copy(b[12:], "VP8X")
	b[24], b[25], b[26] = 0xFF, 0x0F, 0x00 // 4095 → width 4096
	b[27], b[28], b[29] = 0xFF, 0x07, 0x00 // 2047 → height 2048
	w, h := imageSize(b)
	if w != 4096 || h != 2048 {
		t.Fatalf("got %dx%d, want 4096x2048", w, h)
	}
}

func TestImageSizePNG(t *testing.T) {
	b := make([]byte, 32)
	copy(b[0:], "\x89PNG\r\n\x1a\n")
	b[16], b[17], b[18], b[19] = 0x00, 0x00, 0x0A, 0x00 // 2560
	b[20], b[21], b[22], b[23] = 0x00, 0x00, 0x05, 0xA0 // 1440
	w, h := imageSize(b)
	if w != 2560 || h != 1440 {
		t.Fatalf("got %dx%d, want 2560x1440", w, h)
	}
}

func TestPoolLookup(t *testing.T) {
	if _, ok := poolByKey("ycy"); !ok {
		t.Error("ycy must exist")
	}
	if _, ok := poolByKey("YCY"); !ok {
		t.Error("lookup must be case-insensitive")
	}
	if _, ok := poolByKey("nope"); ok {
		t.Error("unknown pool must not resolve")
	}
	p, _ := poolByKey("ycy")
	if !p.Auto {
		t.Error("ycy is an adaptive (redirect) pool")
	}
	p, _ = poolByKey("pc")
	if p.Auto {
		t.Error("pc is a JSON pool")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
