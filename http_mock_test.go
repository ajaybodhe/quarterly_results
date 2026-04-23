package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
)

// mockTransport is a test http.RoundTripper that returns canned responses for
// URLs matching a substring. Routes are matched in the order they were added;
// first match wins. Use matchFunc for patterns that need more than a substring.
//
// Intended usage:
//
//	tr := newMockTransport()
//	tr.on("data.sec.gov/api/xbrl/companyconcept/CIK0000320193/us-gaap/Revenues.json",
//	      200, `{"units":{"USD":[...]}}`, "application/json")
//	client := &http.Client{Transport: tr}
//	sec := &SECClient{httpClient: client}
type mockTransport struct {
	mu       sync.Mutex
	routes   []mockRoute
	requests []*http.Request // captured in order
}

type mockRoute struct {
	match   func(*http.Request) bool
	respond func(*http.Request) *http.Response
}

func newMockTransport() *mockTransport { return &mockTransport{} }

// on registers a substring-based route. If the request URL (including query)
// contains pathSubstr, respond with status, body, and content-type.
func (t *mockTransport) on(pathSubstr string, status int, body, contentType string) *mockTransport {
	return t.onFunc(
		func(r *http.Request) bool { return strings.Contains(r.URL.String(), pathSubstr) },
		func(r *http.Request) *http.Response {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(bytes.NewBufferString(body)),
				Header:     http.Header{"Content-Type": []string{contentType}},
				Request:    r,
			}
		},
	)
}

// onFunc registers a route with arbitrary matching and response logic.
func (t *mockTransport) onFunc(match func(*http.Request) bool, respond func(*http.Request) *http.Response) *mockTransport {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.routes = append(t.routes, mockRoute{match: match, respond: respond})
	return t
}

// count returns how many times RoundTrip was called for URLs matching substr.
func (t *mockTransport) count(substr string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, r := range t.requests {
		if strings.Contains(r.URL.String(), substr) {
			n++
		}
	}
	return n
}

// RoundTrip implements http.RoundTripper.
func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req)
	routes := t.routes
	t.mu.Unlock()

	for _, r := range routes {
		if r.match(req) {
			return r.respond(req), nil
		}
	}
	// Default: 404 so tests fail loudly when a route is missing.
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(bytes.NewBufferString("no mock route for " + req.URL.String())),
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Request:    req,
	}, nil
}

// newMockClient returns an *http.Client that uses the given mockTransport.
func newMockClient(t *mockTransport) *http.Client {
	return &http.Client{Transport: t}
}
