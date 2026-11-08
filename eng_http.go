package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Helpers of the engines reached through an HTTPS API (Couchbase, Cosmos
// DB, Weaviate, BigQuery, Athena).

// apiClient makes the requests of one connection.
func apiClient(opts map[string]string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if optBool(opts, "insecure", false) {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	tr.ResponseHeaderTimeout = 5 * time.Minute
	return &http.Client{Transport: tr}
}

// baseURL makes http(s)://host:port from a host that may already be a URL.
func baseURL(host string, port, def int, useTLS bool) string {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return host
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	return scheme + "://" + hostPort(host, port, def)
}

// doJSON sends body (nil, []byte or a value to marshal) and decodes the
// JSON answer into out (unless nil). An error answer becomes an error.
func doJSON(ctx context.Context, c *http.Client, method, url string, header http.Header, body, out any) (http.Header, error) {
	resp, err := sendJSON(ctx, c, method, url, header, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return resp.Header, httpError(resp.Status, b)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.Header, fmt.Errorf("unexpected answer from %s: %w", url, err)
		}
	}
	return resp.Header, nil
}

// sendJSON sends a request and returns the open response.
func sendJSON(ctx context.Context, c *http.Client, method, url string, header http.Header, body any) (*http.Response, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if rd != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "cactusdb/"+version)
	return c.Do(req)
}

// checkWords refuses q unless its first word is one of starts and no other
// word is in forbidden (after blanking quoted text and comments).
func checkWords(q, quotes string, starts []string, forbidden *regexp.Regexp, readOnlyErr error) error {
	code := blankQuoted(stripLineComments(q), quotes)
	code = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(code), ";"))
	if code == "" {
		return errors.New("the query is empty")
	}
	if strings.Contains(code, ";") {
		return fmt.Errorf("%w; run one statement at a time", readOnlyErr)
	}
	first := firstWord(code)
	ok := false
	for _, s := range starts {
		if first == s {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("%w (the statement starts with %s)", readOnlyErr, first)
	}
	if m := forbidden.FindString(code); m != "" {
		return fmt.Errorf("%w (found %s)", readOnlyErr, strings.ToUpper(m))
	}
	return nil
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}
