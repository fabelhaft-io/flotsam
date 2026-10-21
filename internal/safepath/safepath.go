// Package safepath checks that paths inside a snapshot can't escape it and are
// valid file names on Linux, macOS and Windows.
//
// Snapshot paths are often built from data the service returns: repository
// names, attachment file names, page titles. That data is controlled by
// whoever can create content in the account, so an attachment named
// "../../.ssh/authorized_keys" must not be able to write outside the backup.
// And a backup made on Linux has to stay usable when it is copied to Windows,
// which rejects names Linux accepts, like "a:b" or "con.txt". Checking every
// path in one place means no provider has to remember these rules.
package safepath

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// maximumSegmentLength is the longest file or directory name that common file
// systems accept.
const maximumSegmentLength = 255

// Validate returns an error unless path is a relative, forward-slash path whose
// segments are all safe file names. It rejects rather than repairs unsafe
// paths: silently changing a name could map two different items to the same
// file. Providers that store names from the service should encode them first.
func Validate(path string) error {
	switch {
	case path == "":
		return errors.New("path is empty")
	case !utf8.ValidString(path):
		return fmt.Errorf("path %q is not valid UTF-8", path)
	case strings.HasPrefix(path, "/"):
		return fmt.Errorf("path %q must be relative", path)
	case strings.Contains(path, `\`):
		return fmt.Errorf("path %q must use forward slashes", path)
	}
	for segment := range strings.SplitSeq(path, "/") {
		if err := validateSegment(segment); err != nil {
			return fmt.Errorf("path %q: %w", path, err)
		}
	}
	return nil
}

// validateSegment checks one file or directory name.
func validateSegment(segment string) error {
	switch {
	case segment == "":
		return errors.New("empty segment")
	case segment == "." || segment == "..":
		return fmt.Errorf("segment %q is not allowed", segment)
	case len(segment) > maximumSegmentLength:
		return fmt.Errorf("segment is longer than %d bytes", maximumSegmentLength)
	case strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " "):
		return fmt.Errorf("segment %q ends with a dot or space, which Windows drops", segment)
	case isReservedOnWindows(segment):
		return fmt.Errorf("segment %q is a reserved name on Windows", segment)
	}
	// Control characters are allowed on Linux but break terminals and many
	// tools; the others are forbidden in Windows file names.
	for _, character := range segment {
		if character < 0x20 || strings.ContainsRune(`<>:"|?*`, character) {
			return fmt.Errorf("segment %q contains %q, which Windows doesn't allow in file names", segment, character)
		}
	}
	return nil
}

// isReservedOnWindows reports whether segment is a device name such as CON or
// COM1, which Windows reserves with or without an extension.
func isReservedOnWindows(segment string) bool {
	base, _, _ := strings.Cut(segment, ".")
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 &&
		(strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '0' && base[3] <= '9'
}
