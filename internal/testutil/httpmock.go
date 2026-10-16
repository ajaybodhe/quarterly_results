package testutil

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
)

// MockTransport is a test http.RoundTripper that returns canned responses for
// URLs matching a substring. Routes are matched in the order they were added;
// first match wins. Use matchFunc for patterns that need more than a substring.
//
// Intended usage:
//
//	tr := NewMockTransport()
//	tr.On("data.sec.gov/api/xbrl/companyconcept/CIK0000320193/us-gaap/Revenues.json",
//	      200, `{"units":{"USD":[...]}}`, "application/json")
//	client := &http.Client{Transport: tr}
//	c := sec.NewClientWithHTTP(client, nil)
type MockTransport struct {
	mu       sync.Mutex
	routes   []mockRoute
	requests []*http.Request // captured in order
}

type mockRoute struct {
	match   func(*http.Request) bool
	respond func(*http.Request) *http.Response
}

func NewMockTransport() *MockTransport { return &MockTransport{} }

// on registers a substring-based route. If the request URL (including query)
// contains pathSubstr, respond with status, body, and content-type.
func (t *MockTransport) On(pathSubstr string, status int, body, contentType string) *MockTransport {
	return t.OnFunc(
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

// OnFunc registers a route with arbitrary matching and response logic.
func (t *MockTransport) OnFunc(match func(*http.Request) bool, respond func(*http.Request) *http.Response) *MockTransport {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.routes = append(t.routes, mockRoute{match: match, respond: respond})
	return t
}

// count returns how many times RoundTrip was called for URLs matching substr.
func (t *MockTransport) Count(substr string) int {
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
func (t *MockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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

// NewMockClient returns an *http.Client that uses the given MockTransport.
func NewMockClient(t *MockTransport) *http.Client {
	return &http.Client{Transport: t}
}
