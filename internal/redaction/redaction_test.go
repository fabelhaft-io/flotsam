package redaction

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const secret = "ghp_s3cret/value+1"

func TestString(test *testing.T) {
	redactor := New(secret, "")
	tests := []struct {
		text string
		want string
	}{
		{text: "token " + secret, want: "token [redacted]"},
		{text: "https://api.example/?token=ghp_s3cret%2Fvalue%2B1", want: "https://api.example/?token=[redacted]"},
		{text: "nothing to hide", want: "nothing to hide"},
	}
	for _, testCase := range tests {
		if got := redactor.String(testCase.text); got != testCase.want {
			test.Errorf("String(%q) = %q, want %q", testCase.text, got, testCase.want)
		}
	}
}

func TestStringReplacesLongerSecretsFirst(test *testing.T) {
	redactor := New("abc", "abcdef")
	if got := redactor.String("abcdef"); got != Replacement {
		test.Errorf("String(abcdef) = %q, want %q", got, Replacement)
	}
}

func TestError(test *testing.T) {
	redactor := New(secret)
	clean := errors.New("clean")
	if got := redactor.Error(clean); got != clean { //nolint:errorlint // the same error value must come back
		test.Errorf("Error(clean) returned a different error: %v", got)
	}
	got := redactor.Error(fmt.Errorf("request failed: %s", secret))
	if strings.Contains(got.Error(), secret) {
		test.Errorf("Error() = %q, still contains the secret", got)
	}
	if redactor.Error(nil) != nil {
		test.Error("Error(nil) != nil")
	}
}

func TestHandler(test *testing.T) {
	var output bytes.Buffer
	redactor := New(secret)
	logger := slog.New(redactor.Handler(slog.NewTextHandler(&output, nil)))

	logger.With("preset", secret).WithGroup("group").Info(
		"message "+secret,
		"string", secret,
		"error", errors.New("failed with "+secret),
		slog.Group("nested", "value", secret),
		"number", 42,
	)

	if strings.Contains(output.String(), "s3cret") {
		test.Errorf("log output contains the secret: %s", output.String())
	}
	if !strings.Contains(output.String(), "number=42") {
		test.Errorf("log output lost a non-secret attribute: %s", output.String())
	}
}

func TestHandlerWithoutSecretsIsUnchanged(test *testing.T) {
	next := slog.NewTextHandler(&bytes.Buffer{}, nil)
	if got := New().Handler(next); got != next {
		test.Error("Handler() wrapped the handler although there are no secrets")
	}
}
