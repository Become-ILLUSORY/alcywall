package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Picture is one entry returned by the 栗次元 JSON API.
type Picture struct {
	ID   string `json:"id"`
	Link string `json:"link"`
}

// jsonResponse mirrors https://t.alcy.cc/json?<category>[=<n>].
//
//	count=1 → "data": { ... }        (single object)
//	count>1 → "data": [ { ... }, … ] (array)
//
// so Data is decoded lazily through RawMessage.
type jsonResponse struct {
	Code     int             `json:"code"`
	Category string          `json:"category"`
	Count    int             `json:"count"`
	Data     json.RawMessage `json:"data"`
}

// Pictures unpacks Data into a slice, tolerating both shapes.
func (r jsonResponse) Pictures() ([]Picture, error) {
	trimmed := strings.TrimSpace(string(r.Data))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var many []Picture
		if err := json.Unmarshal(r.Data, &many); err != nil {
			return nil, fmt.Errorf("decode picture array: %w", err)
		}
		return many, nil
	}
	var one Picture
	if err := json.Unmarshal(r.Data, &one); err != nil {
		return nil, fmt.Errorf("decode picture object: %w", err)
	}
	return []Picture{one}, nil
}

// apiError is returned for non-2xx responses so callers can classify failures.
type apiError struct {
	Status int
	URL    string
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("http %d from %s: %s", e.Status, e.URL, truncate(e.Body, 160))
}

// Client talks to the 栗次元 API.
type Client struct {
	base string
	http *http.Client
	log  *logger
}

// NewClient builds an API client. Redirects are never followed: the adaptive
// pools answer with a 302 whose Location header is the picture URL, and
// following it would download the whole image just to read a header.
func NewClient(cfg Config, hc *http.Client, log *logger) *Client {
	noRedirect := *hc
	noRedirect.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{base: strings.TrimRight(cfg.BaseURL, "/"), http: &noRedirect, log: log}
}

// jsonURL builds {base}/json?<category>[=<count>].
//
// The API's own docs put the count directly on the category key
// ("/json?pc=3"), so we do the same instead of inventing a count parameter.
func (c *Client) jsonURL(category string, count int) string {
	if count > 1 {
		return fmt.Sprintf("%s/json?%s=%d", c.base, category, count)
	}
	return c.base + "/json?" + category
}

// Search asks for `count` random pictures from `category`.
//
// Two API surfaces exist and both are used:
//
//	/json?<category>[=<n>]  JSON with ids and links — every fixed pool
//	/<category>             302 redirect — the adaptive pools (ycy/moez/…)
//
// The adaptive pools have no JSON endpoint, so for them we follow the redirect
// manually and read the Location header, which is exactly the picture URL. The
// count parameter is meaningless there (one picture per request), so a batch
// request simply becomes `count` redirect probes.
func (c *Client) Search(ctx context.Context, category string, count int) ([]Picture, error) {
	if count < 1 {
		count = 1
	}
	pool, ok := poolByKey(category)
	if ok && pool.Auto {
		return c.searchRedirect(ctx, category, count)
	}

	raw := c.jsonURL(category, count)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", raw, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", raw, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &apiError{Status: resp.StatusCode, URL: raw, Body: string(body)}
	}

	var jr jsonResponse
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, fmt.Errorf("decode %s: %w (body: %s)", raw, err, truncate(string(body), 120))
	}
	if jr.Code != 0 && jr.Code != 200 {
		return nil, fmt.Errorf("api error %d from %s", jr.Code, raw)
	}
	pics, err := jr.Pictures()
	if err != nil {
		return nil, err
	}
	if len(pics) == 0 {
		return nil, fmt.Errorf("empty result for category %q", category)
	}
	c.log.Debugf("api: %d picture(s) from %s", len(pics), raw)
	return pics, nil
}

// searchRedirect resolves the adaptive pools through their 302 redirect.
func (c *Client) searchRedirect(ctx context.Context, category string, count int) ([]Picture, error) {
	raw := c.base + "/" + category
	out := make([]Picture, 0, count)
	seen := map[string]bool{}
	// Each request returns one picture; retry a little for a fresh id.
	for attempt := 0; attempt < count*2 && len(out) < count; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "image/*")

		resp, err := c.http.Do(req)
		if err != nil {
			if len(out) > 0 {
				break
			}
			return nil, fmt.Errorf("request %s: %w", raw, err)
		}
		loc := resp.Header.Get("Location")
		resp.Body.Close()

		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			if resp.StatusCode >= 400 {
				return nil, &apiError{Status: resp.StatusCode, URL: raw}
			}
			// Already an image (no redirect): use the request URL itself.
			loc = raw
		}
		link := absoluteURL(c.base, loc)
		id := pictureIDFromLink(link)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Picture{ID: id, Link: link})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty result for category %q", category)
	}
	c.log.Debugf("api: %d picture(s) from %s (redirect)", len(out), raw)
	return out, nil
}

// absoluteURL resolves a possibly-relative Location against the API base.
func absoluteURL(base, loc string) string {
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		return loc
	}
	if strings.HasPrefix(loc, "/") {
		return strings.TrimRight(base, "/") + loc
	}
	return strings.TrimRight(base, "/") + "/" + loc
}

// pictureIDFromLink derives a stable id from a picture URL, which is the last
// path segment without its query string (e.g. "2025-10-abc…webp").
func pictureIDFromLink(link string) string {
	if u, err := url.Parse(link); err == nil && u.Path != "" {
		link = u.Path
	}
	link = strings.TrimRight(link, "/")
	if i := strings.LastIndexByte(link, '/'); i >= 0 {
		link = link[i+1:]
	}
	return link
}

// sizedLink appends the ?w= / ?h= scaling parameters to a picture link.
//
// Verified behaviour of the service (2026-10):
//   - ?w=N scales the image down to N pixels wide; the height follows the
//     aspect ratio. A value >= the original width returns the original.
//   - N > 4096 answers 404 page not found, so we clamp to maxAPISize.
//   - The parameters are accepted on the bare /pic/... links too, so a URL
//     fetched from the JSON API can be re-requested at any size.
func sizedLink(link string, width, height int) string {
	if width <= 0 && height <= 0 {
		return link
	}
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	q := u.Query()
	if width > 0 {
		if width > maxAPISize {
			width = maxAPISize
		}
		q.Set("w", fmt.Sprintf("%d", width))
	}
	if height > 0 {
		if height > maxAPISize {
			height = maxAPISize
		}
		q.Set("h", fmt.Sprintf("%d", height))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// IsVideoLink reports whether a picture link is actually a video file
// (the /acg pool serves .mp4) and therefore unusable as a wallpaper.
func IsVideoLink(link string) bool {
	low := strings.ToLower(link)
	for _, ext := range []string{".mp4", ".webm", ".mov", ".gif"} {
		if strings.HasSuffix(low, ext) || strings.Contains(low, ext+"?") {
			return true
		}
	}
	return false
}

// downloadFile streams url into dst, enforcing the size guards. It returns the
// number of bytes written.
func downloadFile(ctx context.Context, hc *http.Client, rawURL, dst string, minBytes, maxBytes int64, log *logger) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "image/*")
	req.Header.Set("User-Agent", userAgent)

	started := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("get %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, &apiError{Status: resp.StatusCode, URL: rawURL, Body: string(body)}
	}

	ctype := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if ctype != "" && !strings.HasPrefix(ctype, "image/") {
		return 0, fmt.Errorf("unexpected content-type %q from %s", ctype, rawURL)
	}
	if resp.ContentLength > 0 && maxBytes > 0 && resp.ContentLength > maxBytes {
		return 0, fmt.Errorf("image too large: %d bytes", resp.ContentLength)
	}

	f, err := createTempFile(dst)
	if err != nil {
		return 0, err
	}
	tmpName := f.Name()
	written, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := f.Close()
	if err != nil {
		removeFile(tmpName)
		return 0, fmt.Errorf("write %s: %w", tmpName, err)
	}
	if closeErr != nil {
		removeFile(tmpName)
		return 0, closeErr
	}
	if written > maxBytes {
		removeFile(tmpName)
		return 0, fmt.Errorf("image exceeds %d bytes", maxBytes)
	}
	if written < minBytes {
		removeFile(tmpName)
		return 0, fmt.Errorf("image too small: %d bytes (probably an error page)", written)
	}
	if err := moveFile(tmpName, dst); err != nil {
		removeFile(tmpName)
		return 0, fmt.Errorf("finalise %s: %w", dst, err)
	}
	log.Debugf("downloaded %s in %s", humanBytes(written), time.Since(started).Round(time.Millisecond))
	return written, nil
}

// sanitizeName turns a picture id into a safe file name.
func sanitizeName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "wallpaper"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := b.String()
	name = strings.TrimLeft(name, ".")
	if name == "" {
		name = "wallpaper"
	}
	if len(name) > 96 {
		name = name[:96]
	}
	// Keep the extension, add a stable prefix so pruning can spot our files.
	return "alcy-" + name
}
