// Package httpclient builds the HTTP client that providers get from
// provider.Env. It retries transient failures, waits out rate limits and never
// logs headers or query strings.
//
// A backup makes thousands of requests against APIs that rate-limit hard and
// occasionally fail. Without retries, one 502 out of ten thousand requests
// would fail a whole backup that ran for an hour. Every provider needs the
// same handling, and getting it right is fiddly: which statuses to retry, how
// to read each service's rate-limit headers, how to replay a request body. So
// it lives here once, in the transport, where providers get it without doing
// anything, instead of in retry loops scattered through every provider.
package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	// Five attempts with doubling backoff cover about a minute of trouble,
	// enough for a deploy or a short outage, without hanging for long on a
	// service that is really down.
	maximumAttempts = 5
	// maximumWait is the longest single wait for a rate limit. Primary rate
	// limits (GitHub's included) reset within an hour. Waiting that long is
	// better for a scheduled backup than failing, which would mean no backup
	// until the next run. If a service asks for longer, the rate-limited
	// response is returned.
	maximumWait    = time.Hour
	firstBackoff   = time.Second
	maximumBackoff = 30 * time.Second
	// responseHeaderTimeout catches servers that accept a request and then
	// never answer. It only covers the wait for the headers, so slow but
	// steady downloads of large files are not cut off.
	responseHeaderTimeout = 2 * time.Minute
	// drainLimit is how much of a discarded response body is read so the
	// connection can be reused.
	drainLimit = 64 << 10
)

// Options configure New. The zero value works.
type Options struct {
	// UserAgent is sent with every request that doesn't set its own.
	UserAgent string
	Logger    *slog.Logger
	// Transport sends the requests. Default: http.DefaultTransport with a
	// timeout for response headers.
	Transport http.RoundTripper
	// Sleep and Now replace the clock in tests, so tests of an hour-long rate
	// limit run instantly.
	Sleep func(ctx context.Context, duration time.Duration) error
	Now   func() time.Time
}

// New returns a client that retries requests which failed with a network
// error, 429 Too Many Requests, a rate-limited 403, or 500, 502, 503 and 504.
// It honors the Retry-After and X-RateLimit-* headers.
//
// The client has no overall timeout, so large downloads can take as long as
// they need; use the request context to limit them.
func New(options Options) *http.Client {
	transport := &retryTransport{
		next:      options.Transport,
		userAgent: options.UserAgent,
		logger:    options.Logger,
		sleep:     options.Sleep,
		now:       options.Now,
	}
	if transport.next == nil {
		defaultTransport := http.DefaultTransport.(*http.Transport).Clone()
		defaultTransport.ResponseHeaderTimeout = responseHeaderTimeout
		transport.next = defaultTransport
	}
	if transport.logger == nil {
		transport.logger = slog.New(slog.DiscardHandler)
	}
	if transport.sleep == nil {
		transport.sleep = sleep
	}
	if transport.now == nil {
		transport.now = time.Now
	}
	return &http.Client{Transport: transport}
}

// retryTransport is an http.RoundTripper that wraps another one and repeats
// requests. Retrying in the transport, not around client.Do, means providers
// use a plain *http.Client and every request is covered automatically.
type retryTransport struct {
	next      http.RoundTripper
	userAgent string
	logger    *slog.Logger
	sleep     func(ctx context.Context, duration time.Duration) error
	now       func() time.Time
}

// RoundTrip sends request, retrying as decide says.
func (transport *retryTransport) RoundTrip(original *http.Request) (*http.Response, error) {
	// A RoundTripper must not change the caller's request.
	request := original.Clone(original.Context())
	if request.Header.Get("User-Agent") == "" && transport.userAgent != "" {
		request.Header.Set("User-Agent", transport.userAgent)
	}

	for attempt := 1; ; attempt++ {
		response, err := transport.next.RoundTrip(request)
		wait, reason, retry := transport.decide(request, response, err, attempt)
		if !retry {
			return response, err
		}
		// Reading the rest of the body lets the connection be reused for the
		// retry instead of opening a new one.
		if response != nil {
			_, _ = io.CopyN(io.Discard, response.Body, drainLimit)
			_ = response.Body.Close()
		}
		// Only the host and path are logged. Headers carry the token, and
		// query strings sometimes do too.
		transport.logger.Warn("retrying request",
			"method", request.Method,
			"host", request.URL.Host,
			"path", request.URL.Path,
			"reason", reason,
			"wait", wait.Round(time.Second),
			"attempt", attempt+1,
		)
		if err := transport.sleep(request.Context(), wait); err != nil {
			return nil, err
		}
		// The previous attempt consumed the body, so a fresh copy is needed.
		if request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return nil, err
			}
			request.Body = body
		}
	}
}

// decide returns whether to retry, how long to wait first, and a reason for
// the log.
func (transport *retryTransport) decide(request *http.Request, response *http.Response, err error, attempt int) (time.Duration, string, bool) {
	if attempt >= maximumAttempts || !canReplay(request) || request.Context().Err() != nil {
		return 0, "", false
	}
	if err != nil {
		if !isTransient(err) {
			return 0, "", false
		}
		return backoff(attempt), "network error: " + err.Error(), true
	}

	wait, limited := rateLimitWait(response.Header, transport.now())
	switch {
	// GitHub answers an exhausted rate limit with 403 instead of 429. A 403
	// without rate-limit headers is a real permission problem, which waiting
	// won't fix, so it is returned to the provider.
	case response.StatusCode == http.StatusTooManyRequests,
		response.StatusCode == http.StatusForbidden && limited:
		if !limited {
			wait = backoff(attempt)
		}
		// Give up instead of hanging for many hours: the provider gets the
		// 429 and can report it, and the next scheduled run tries again.
		if wait > maximumWait {
			transport.logger.Warn("rate limit resets later than flotsam waits",
				"host", request.URL.Host, "wait", wait.Round(time.Second), "maximum", maximumWait)
			return 0, "", false
		}
		return wait, "rate limited", true
	case isRetryableStatus(response.StatusCode):
		if !limited {
			wait = backoff(attempt)
		}
		if wait > maximumWait {
			return 0, "", false
		}
		return wait, response.Status, true
	}
	return 0, "", false
}

// canReplay reports whether the request body can be sent again. Requests
// built from a bytes.Reader or strings.Reader can; a body read from a stream
// could only be sent once.
func canReplay(request *http.Request) bool {
	return request.Body == nil || request.Body == http.NoBody || request.GetBody != nil
}

// isRetryableStatus reports whether a status means "try again later". 501
// Not Implemented is a 5xx too, but it never changes, so it isn't retried.
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// isTransient reports whether a network error may go away by itself, such as
// a reset connection. Canceled requests and certificate errors won't, and
// retrying them would only delay the error.
func isTransient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var certificateError *tls.CertificateVerificationError
	if errors.As(err, &certificateError) {
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// rateLimitWait reads how long the service asks clients to wait. It returns
// false if the response says nothing about it. Services differ in how they
// say it, so the common forms are all understood; waiting exactly as long as
// asked is both faster and politer than guessing with backoff.
func rateLimitWait(header http.Header, now time.Time) (time.Duration, bool) {
	if value := header.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second, true
		}
		if date, err := http.ParseTime(value); err == nil {
			return max(date.Sub(now), 0), true
		}
	}
	// GitHub and many others: the Unix time when the limit resets.
	if header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return max(time.Unix(reset, 0).Sub(now), 0) + time.Second, true
		}
	}
	return 0, false
}

// backoff doubles the wait with each attempt, up to maximumBackoff, plus up to
// 25% jitter so many clients don't retry in lockstep.
func backoff(attempt int) time.Duration {
	wait := min(firstBackoff<<(attempt-1), maximumBackoff)
	jitter := time.Duration(rand.Int64N(int64(wait / 4))) //nolint:gosec // jitter doesn't need a cryptographic random source
	return wait + jitter
}

// sleep waits for duration, or less if ctx ends first, so Ctrl-C works even
// during an hour-long rate limit wait.
func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
