package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// api sends requests to the GitHub REST API. Retries and rate limits are
// already handled by the client from provider.Env.
type api struct {
	client *http.Client
	token  string
}

// statusError is a response other than 200 OK. It keeps GitHub's own message,
// which often says exactly what is wrong, such as "Resource not accessible by
// personal access token".
type statusError struct {
	status  int
	message string
}

// Error implements error.
func (failure *statusError) Error() string {
	if failure.message == "" {
		return fmt.Sprintf("%d %s", failure.status, http.StatusText(failure.status))
	}
	return fmt.Sprintf("%d %s: %s", failure.status, http.StatusText(failure.status), failure.message)
}

// isStatus reports whether err is a response with the given status.
func isStatus(err error, status int) bool {
	var failure *statusError
	return errors.As(err, &failure) && failure.status == status
}

// get fetches one object and decodes it into target.
func (client api) get(ctx context.Context, address string, target any) error {
	response, err := client.request(ctx, address)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	return json.NewDecoder(response.Body).Decode(target)
}

// each fetches every page of a list endpoint and passes the items to handle
// one by one, so only one page is in memory at a time.
func (client api) each(ctx context.Context, address string, handle func(item json.RawMessage) error) error {
	// 100 is the largest page GitHub allows; it means a hundred times fewer
	// requests against the rate limit than the default of 30.
	separator := "?"
	if strings.Contains(address, "?") {
		separator = "&"
	}
	address += separator + "per_page=100"

	for address != "" {
		response, err := client.request(ctx, address)
		if err != nil {
			return err
		}
		var items []json.RawMessage
		err = json.NewDecoder(response.Body).Decode(&items)
		_ = response.Body.Close()
		if err != nil {
			return fmt.Errorf("decode %s: %w", address, err)
		}
		for _, item := range items {
			if err := handle(item); err != nil {
				return err
			}
		}
		if address, err = nextPage(response.Header); err != nil {
			return err
		}
	}
	return nil
}

// request sends a GET request and returns the response if it is 200 OK.
func (client api) request(ctx context.Context, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if client.token != "" {
		request.Header.Set("Authorization", "Bearer "+client.token)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusOK {
		return response, nil
	}
	defer func() { _ = response.Body.Close() }()
	var body struct {
		Message string `json:"message"`
	}
	// The body is small for errors; reading at most 64 KiB keeps a broken
	// response from filling memory.
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body)
	if response.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("GitHub rejected the token: %w", &statusError{status: response.StatusCode, message: body.Message})
	}
	return nil, &statusError{status: response.StatusCode, message: body.Message}
}

// nextPage returns the URL of the next page from a Link header, or "" on the
// last page. The URL must point at the API itself: the token goes with every
// request, so following a link to another host would send it there.
func nextPage(header http.Header) (string, error) {
	for link := range strings.SplitSeq(header.Get("Link"), ",") {
		address, relation, found := strings.Cut(link, ";")
		if !found || strings.TrimSpace(relation) != `rel="next"` {
			continue
		}
		address = strings.Trim(strings.TrimSpace(address), "<>")
		parsed, err := url.Parse(address)
		if err != nil || parsed.Scheme+"://"+parsed.Host != apiURL {
			return "", fmt.Errorf("next page %q is not on %s", address, apiURL)
		}
		return address, nil
	}
	return "", nil
}
