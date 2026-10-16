package yahoo

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"
)

// Client is the shared Yahoo Finance session. Yahoo requires a crumb token
// tied to a cookie session, so http holds a cookie jar and is used exclusively
// for Yahoo API calls; the crumb is fetched lazily, once per process.
type Client struct {
	http      *http.Client
	crumb     string
	crumbOnce sync.Once
	crumbErr  error
}

func NewClient() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{http: &http.Client{Timeout: 30 * time.Second, Jar: jar}}
}

// HTTP returns the cookie-jar client so other Yahoo endpoints share the session.
func (y *Client) HTTP() *http.Client { return y.http }

// ensureCrumb fetches a Yahoo Finance session cookie + crumb (done once, cached).
// Yahoo Finance requires a valid crumb tied to a cookie session for all API requests.
//
// Flow:
//  1. GET https://fc.yahoo.com — returns 404 but sets the A3 session cookie via HTTP headers.
//     This cookie is required by the crumb and options endpoints.
//  2. GET https://query2.finance.yahoo.com/v1/test/getcrumb — returns the crumb string.
//
// Both calls share the client's http.Client (which has a cookie jar), so the A3 cookie is automatically
// sent with the crumb request and all subsequent options API calls.
func (y *Client) ensureCrumb() error {
	y.crumbOnce.Do(func() {
		ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

		// Step 1: hit fc.yahoo.com to receive the A3 session cookie via Set-Cookie headers.
		// fc.yahoo.com returns HTTP 404 but the cookie is still set — this is intentional.
		req1, _ := http.NewRequest("GET", "https://fc.yahoo.com", nil)
		req1.Header.Set("User-Agent", ua)
		resp1, err := y.http.Do(req1)
		if err != nil {
			y.crumbErr = fmt.Errorf("yahoo A3 cookie: %w", err)
			return
		}
		resp1.Body.Close()

		// Step 2: fetch the crumb using the A3 cookie now in the jar.
		req2, _ := http.NewRequest("GET", "https://query2.finance.yahoo.com/v1/test/getcrumb", nil)
		req2.Header.Set("User-Agent", ua)
		req2.Header.Set("Accept", "text/plain")
		req2.Header.Set("Referer", "https://finance.yahoo.com/")
		resp2, err := y.http.Do(req2)
		if err != nil {
			y.crumbErr = fmt.Errorf("yahoo crumb: %w", err)
			return
		}
		defer resp2.Body.Close()
		body, _ := io.ReadAll(resp2.Body)
		crumb := strings.TrimSpace(string(body))
		if resp2.StatusCode != http.StatusOK || crumb == "" {
			y.crumbErr = fmt.Errorf("yahoo crumb failed (HTTP %d): %s", resp2.StatusCode, crumb)
			return
		}
		y.crumb = crumb
	})
	return y.crumbErr
}
