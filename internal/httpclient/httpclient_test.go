package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scriptedTransport answers each request with the next response in its list.
type scriptedTransport struct {
	responses []func(request *http.Request) (*http.Response, error)
	requests  []*http.Request
	bodies    []string
}

// RoundTrip records the request and returns the next scripted response. The
// last response repeats, so "always 500" needs only one entry.
func (script *scriptedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	script.requests = append(script.requests, request)
	if request.Body != nil {
		body, _ := io.ReadAll(request.Body)
		script.bodies = append(script.bodies, string(body))
	}
	next := script.responses[0]
	if len(script.responses) > 1 {
		script.responses = script.responses[1:]
	}
	return next(request)
}

// status returns a response function with the given status and headers,
// given as name, value pairs.
func status(code int, headers ...string) func(*http.Request) (*http.Response, error) {
	return func(request *http.Request) (*http.Response, error) {
		header := http.Header{}
		for index := 0; index+1 < len(headers); index += 2 {
			header.Set(headers[index], headers[index+1])
		}
		return &http.Response{
			StatusCode: code,
			Status:     strconv.Itoa(code) + " " + http.StatusText(code),
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("body")),
			Request:    request,
		}, nil
	}
}

// networkFailure fails like a refused connection.
func networkFailure(*http.Request) (*http.Response, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
}

var now = time.Date(2026, 10, 21, 19, 0, 0, 0, time.UTC)

// newTestClient returns a client whose waits are recorded instead of slept,
// so tests of long rate limits run instantly and can check the wait times.
func newTestClient(transport http.RoundTripper, waits *[]time.Duration) *http.Client {
	return New(Options{
		UserAgent: "flotsam/test",
		Transport: transport,
		Now:       func() time.Time { return now },
		Sleep: func(_ context.Context, duration time.Duration) error {
			*waits = append(*waits, duration)
			return nil
		},
	})
}

// get sends a GET request and closes the response when the test ends. The
// URL carries a token in the query, like some APIs require.
func get(test *testing.T, client *http.Client) *http.Response {
	test.Helper()
	request, err := http.NewRequestWithContext(test.Context(), http.MethodGet, "https://api.example/items?token=secret", nil)
	if err != nil {
		test.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		test.Fatal(err)
	}
	test.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestRetries(test *testing.T) {
	tests := []struct {
		name       string
		responses  []func(*http.Request) (*http.Response, error)
		wantStatus int
		wantWaits  []time.Duration // zero means "any backoff"
	}{
		{
			name:       "success is not retried",
			responses:  []func(*http.Request) (*http.Response, error){status(200)},
			wantStatus: 200,
		},
		{
			name:       "server errors are retried",
			responses:  []func(*http.Request) (*http.Response, error){status(503), status(502), status(200)},
			wantStatus: 200,
			wantWaits:  []time.Duration{0, 0},
		},
		{
			name:       "network errors are retried",
			responses:  []func(*http.Request) (*http.Response, error){networkFailure, status(200)},
			wantStatus: 200,
			wantWaits:  []time.Duration{0},
		},
		{
			name:       "Retry-After in seconds",
			responses:  []func(*http.Request) (*http.Response, error){status(429, "Retry-After", "7"), status(200)},
			wantStatus: 200,
			wantWaits:  []time.Duration{7 * time.Second},
		},
		{
			name:       "Retry-After as a date",
			responses:  []func(*http.Request) (*http.Response, error){status(503, "Retry-After", now.Add(time.Minute).Format(http.TimeFormat)), status(200)},
			wantStatus: 200,
			wantWaits:  []time.Duration{time.Minute},
		},
		{
			name: "GitHub-style rate limit on 403",
			responses: []func(*http.Request) (*http.Response, error){
				status(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)),
				status(200),
			},
			wantStatus: 200,
			wantWaits:  []time.Duration{10*time.Minute + time.Second},
		},
		{
			name:       "plain 403 is a permission problem, not retried",
			responses:  []func(*http.Request) (*http.Response, error){status(403), status(200)},
			wantStatus: 403,
		},
		{
			name:       "404 is not retried",
			responses:  []func(*http.Request) (*http.Response, error){status(404), status(200)},
			wantStatus: 404,
		},
		{
			name:       "501 is not retried",
			responses:  []func(*http.Request) (*http.Response, error){status(501), status(200)},
			wantStatus: 501,
		},
		{
			name:       "a wait longer than the maximum returns the response",
			responses:  []func(*http.Request) (*http.Response, error){status(429, "Retry-After", "7200"), status(200)},
			wantStatus: 429,
		},
		{
			name:       "gives up after the maximum attempts",
			responses:  []func(*http.Request) (*http.Response, error){status(500)},
			wantStatus: 500,
			wantWaits:  []time.Duration{0, 0, 0, 0},
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			transport := &scriptedTransport{responses: testCase.responses}
			var waits []time.Duration
			response := get(test, newTestClient(transport, &waits))

			if response.StatusCode != testCase.wantStatus {
				test.Errorf("status = %d, want %d", response.StatusCode, testCase.wantStatus)
			}
			if len(waits) != len(testCase.wantWaits) {
				test.Fatalf("waited %v, want %d waits", waits, len(testCase.wantWaits))
			}
			for index, want := range testCase.wantWaits {
				if want == 0 {
					if waits[index] < firstBackoff || waits[index] > maximumBackoff*5/4 {
						test.Errorf("wait %d = %v, want a backoff between %v and %v", index, waits[index], firstBackoff, maximumBackoff*5/4)
					}
				} else if waits[index] != want {
					test.Errorf("wait %d = %v, want %v", index, waits[index], want)
				}
			}
		})
	}
}

func TestSetsUserAgentWithoutChangingTheRequest(test *testing.T) {
	transport := &scriptedTransport{responses: []func(*http.Request) (*http.Response, error){status(200)}}
	var waits []time.Duration
	client := newTestClient(transport, &waits)

	request, err := http.NewRequestWithContext(test.Context(), http.MethodGet, "https://api.example/", nil)
	if err != nil {
		test.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		test.Fatal(err)
	}
	_ = response.Body.Close()

	if got := transport.requests[0].Header.Get("User-Agent"); got != "flotsam/test" {
		test.Errorf("User-Agent = %q, want flotsam/test", got)
	}
	if request.Header.Get("User-Agent") != "" {
		test.Error("the caller's request was changed")
	}
}

func TestReplaysTheBodyOnRetry(test *testing.T) {
	transport := &scriptedTransport{responses: []func(*http.Request) (*http.Response, error){status(503), status(200)}}
	var waits []time.Duration
	client := newTestClient(transport, &waits)

	request, err := http.NewRequestWithContext(test.Context(), http.MethodPost, "https://api.example/search", strings.NewReader(`{"query":"x"}`))
	if err != nil {
		test.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		test.Fatal(err)
	}
	_ = response.Body.Close()

	if len(transport.bodies) != 2 || transport.bodies[0] != transport.bodies[1] || transport.bodies[1] != `{"query":"x"}` {
		test.Errorf("bodies sent = %q, want the same body twice", transport.bodies)
	}
}

func TestStopsWaitingWhenTheContextEnds(test *testing.T) {
	transport := &scriptedTransport{responses: []func(*http.Request) (*http.Response, error){status(503)}}
	client := New(Options{Transport: transport})

	ctx, cancel := context.WithTimeout(test.Context(), 50*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example/", nil)
	if err != nil {
		test.Fatal(err)
	}
	started := time.Now()
	response, err := client.Do(request)
	if err == nil {
		_ = response.Body.Close()
		test.Fatal("Do() succeeded, want the context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		test.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		test.Errorf("Do() took %v after the context ended", elapsed)
	}
}
