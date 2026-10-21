// Package redaction removes secret values, such as API tokens, from text, errors
// and log records before anyone can see them.
//
// Providers are written by many people, and any of them can accidentally put a
// token into an error message: an HTTP library error that includes the URL
// with an api_key query parameter is enough. flotsam's output often ends up in
// CI logs, cron mails and bug reports, where a leaked token is public. So the
// engine doesn't rely on every provider being careful: it knows the values of
// all secret settings, and this package removes them from everything flotsam
// prints or writes. It is a safety net; providers should still never log
// secrets on purpose.
package redaction

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// Replacement is written in place of every secret.
const Replacement = "[redacted]"

// Redactor replaces known secret values. A Redactor without secrets changes
// nothing. It matches exact values rather than patterns like "ghp_...",
// because patterns differ per service and would miss tokens of services
// nobody wrote a pattern for.
type Redactor struct {
	// secrets holds every form of every secret, longest first.
	secrets []string
}

// New returns a Redactor for secrets. It also catches the URL-encoded forms of
// each secret, since tokens often end up in query strings or paths, where
// characters such as "/" and "+" are escaped and the plain value wouldn't
// match.
func New(secrets ...string) *Redactor {
	forms := make(map[string]bool)
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		forms[secret] = true
		forms[url.QueryEscape(secret)] = true
		forms[url.PathEscape(secret)] = true
	}
	sorted := slices.Collect(maps.Keys(forms))
	// Longer forms go first, so a secret that contains another one is removed whole.
	slices.SortFunc(sorted, func(first, second string) int {
		return cmp.Compare(len(second), len(first))
	})
	return &Redactor{secrets: sorted}
}

// String returns text with every secret replaced.
func (redactor *Redactor) String(text string) string {
	for _, secret := range redactor.secrets {
		text = strings.ReplaceAll(text, secret, Replacement)
	}
	return text
}

// Error returns err unchanged if its message holds no secret, and otherwise a
// new error with the secrets replaced. The replacement no longer wraps err.
func (redactor *Redactor) Error(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	redacted := redactor.String(message)
	if redacted == message {
		return err
	}
	return errors.New(redacted)
}

// Handler wraps next so every log message and attribute is redacted. The
// engine gives each provider a logger built on it, so even a provider that
// logs a whole request struct can't leak the token in it.
func (redactor *Redactor) Handler(next slog.Handler) slog.Handler {
	if len(redactor.secrets) == 0 {
		return next
	}
	return &redactingHandler{next: next, redactor: redactor}
}

// redactingHandler is a slog.Handler that redacts every record before passing
// it on to the next handler.
type redactingHandler struct {
	next     slog.Handler
	redactor *Redactor
}

// Enabled asks the next handler, so redaction doesn't change which levels are
// logged.
func (handler *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

// Handle redacts the message and every attribute of record. It builds a new
// record instead of changing record, which other handlers may still use.
func (handler *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	redacted := slog.NewRecord(record.Time, record.Level, handler.redactor.String(record.Message), record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		redacted.AddAttrs(handler.attribute(attribute))
		return true
	})
	return handler.next.Handle(ctx, redacted)
}

// WithAttrs redacts attributes added with logger.With, which other handlers
// would store and print without passing them through Handle again.
func (handler *redactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		redacted = append(redacted, handler.attribute(attribute))
	}
	return &redactingHandler{next: handler.next.WithAttrs(redacted), redactor: handler.redactor}
}

// WithGroup keeps redaction in place for loggers created with WithGroup.
func (handler *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: handler.next.WithGroup(name), redactor: handler.redactor}
}

// attribute returns a redacted copy of one attribute.
func (handler *redactingHandler) attribute(attribute slog.Attr) slog.Attr {
	// Resolve calls LogValuer implementations first, so values computed at
	// logging time are redacted too.
	value := attribute.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return slog.String(attribute.Key, handler.redactor.String(value.String()))
	case slog.KindAny:
		// Errors, structs and anything else are turned into their text first,
		// because that text is what the next handler would print.
		return slog.String(attribute.Key, handler.redactor.String(fmt.Sprint(value.Any())))
	case slog.KindGroup:
		group := value.Group()
		redacted := make([]slog.Attr, 0, len(group))
		for _, member := range group {
			redacted = append(redacted, handler.attribute(member))
		}
		return slog.Attr{Key: attribute.Key, Value: slog.GroupValue(redacted...)}
	default:
		// Numbers, booleans, times and durations can't contain a secret.
		return slog.Attr{Key: attribute.Key, Value: value}
	}
}
