// Package kvclient is a typed Go client for the kv HTTP API. HTTP status
// codes are mapped back to the kv package's sentinel errors, so callers use
// errors.Is(err, kv.ErrConflict) exactly as they would with a local Store.
package kvclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Kminhas21/kv/internal/kv"
)

type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for the kv server at baseURL (e.g. "http://localhost:8080").
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// StatusError is returned for any response the client does not map to a
// kv sentinel error. Use errors.As to inspect Code.
type StatusError struct {
	Method, Path string
	Code         int
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: kv returned %d: %s", e.Method, e.Path, e.Code, e.Body)
}

func unexpected(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return &StatusError{
		Method: resp.Request.Method,
		Path:   resp.Request.URL.Path,
		Code:   resp.StatusCode,
		Body:   strings.TrimSpace(string(body)),
	}
}

// Get returns the value and version stored at key, or kv.ErrNotFound.
func (c *Client) Get(ctx context.Context, key string) (value string, ver uint64, err error) {
	resp, err := c.do(ctx, http.MethodGet, keyPath(key), nil, nil)
	if err != nil {
		return "", 0, err
	}
	defer closeBody(resp)

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", 0, fmt.Errorf("read value of %q: %w", key, err)
		}
		ver, err := version(resp)
		return string(body), ver, err
	case http.StatusNotFound:
		return "", 0, fmt.Errorf("get %q: %w", key, kv.ErrNotFound)
	default:
		return "", 0, unexpected(resp)
	}
}

// Put writes value at key and returns the new version. With a non-nil
// ifMatch, a version mismatch returns kv.ErrConflict together with the
// key's current version (0 if it does not exist).
func (c *Client) Put(ctx context.Context, key, value string, ifMatch *uint64) (uint64, error) {
	return c.PutTTL(ctx, key, value, ifMatch, 0)
}

// PutTTL is Put with a TTL: the key expires ttl after the server applies
// the write, and every write resets it. ttl <= 0 makes the key permanent.
func (c *Client) PutTTL(ctx context.Context, key, value string, ifMatch *uint64, ttl time.Duration) (uint64, error) {
	path := keyPath(key)
	if ttl > 0 {
		path += "?ttl=" + url.QueryEscape(ttl.String())
	}
	resp, err := c.do(ctx, http.MethodPut, path, strings.NewReader(value), ifMatch)
	if err != nil {
		return 0, err
	}
	defer closeBody(resp)

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return version(resp)
	case http.StatusConflict:
		current, _ := version(resp)
		return current, fmt.Errorf("put %q: %w", key, kv.ErrConflict)
	default:
		return 0, unexpected(resp)
	}
}

// Delete removes key. Returns kv.ErrNotFound or kv.ErrConflict.
func (c *Client) Delete(ctx context.Context, key string, ifMatch *uint64) error {
	resp, err := c.do(ctx, http.MethodDelete, keyPath(key), nil, ifMatch)
	if err != nil {
		return err
	}
	defer closeBody(resp)

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("delete %q: %w", key, kv.ErrNotFound)
	case http.StatusConflict:
		return fmt.Errorf("delete %q: %w", key, kv.ErrConflict)
	default:
		return unexpected(resp)
	}
}

// List returns every item whose key starts with prefix, sorted by key.
func (c *Client) List(ctx context.Context, prefix string) ([]kv.Item, error) {
	resp, err := c.do(ctx, http.MethodGet, "/keys?prefix="+url.QueryEscape(prefix), nil, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, unexpected(resp)
	}
	var items []kv.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode list %q: %w", prefix, err)
	}
	return items, nil
}

// do sends one request. The caller must close resp.Body.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, ifMatch *uint64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	if ifMatch != nil {
		req.Header.Set("If-Match", strconv.FormatUint(*ifMatch, 10))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// keyPath escapes each segment of key separately, so "/" stays a path
// separator (the server routes on /keys/{key...}) while characters like
// spaces, "?" and "#" cannot break the URL.
func keyPath(key string) string {
	segments := strings.Split(key, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return "/keys/" + strings.Join(segments, "/")
}

// closeBody drains and closes a response body. Draining lets the
// connection go back to the keep-alive pool instead of being torn down.
func closeBody(resp *http.Response) {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func version(resp *http.Response) (uint64, error) {
	v, err := strconv.ParseUint(resp.Header.Get("ETag"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad ETag %q from kv: %w", resp.Header.Get("ETag"), err)
	}
	return v, nil
}
