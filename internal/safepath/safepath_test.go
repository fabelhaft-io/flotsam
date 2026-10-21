package safepath

import (
	"strings"
	"testing"
)

func TestValidate(test *testing.T) {
	tests := []struct {
		path  string
		valid bool
	}{
		{path: "repository.json", valid: true},
		{path: "fabelhaft-io/flotsam/issues/42.json", valid: true},
		{path: "files/report v2.pdf", valid: true},
		{path: "unicode/übersicht.json", valid: true},
		{path: ".hidden", valid: true},
		{path: "", valid: false},
		{path: "/absolute", valid: false},
		{path: `windows\separator`, valid: false},
		{path: "../escape", valid: false},
		{path: "a/../b", valid: false},
		{path: "./a", valid: false},
		{path: "a//b", valid: false},
		{path: "trailing/", valid: false},
		{path: "colon:name", valid: false},
		{path: "question?", valid: false},
		{path: "star*", valid: false},
		{path: "pipe|", valid: false},
		{path: "quote\"", valid: false},
		{path: "angle<bracket>", valid: false},
		{path: "control\x01character", valid: false},
		{path: "trailing.", valid: false},
		{path: "trailing ", valid: false},
		{path: "CON", valid: false},
		{path: "dir/nul.json", valid: false},
		{path: "com1.txt", valid: false},
		{path: "LPT9", valid: false},
		{path: "console.json", valid: true},
		{path: "com10", valid: true},
		{path: "invalid\xffutf8", valid: false},
		{path: strings.Repeat("a", 256), valid: false},
	}
	for _, testCase := range tests {
		err := Validate(testCase.path)
		if testCase.valid && err != nil {
			test.Errorf("Validate(%q) = %v, want no error", testCase.path, err)
		}
		if !testCase.valid && err == nil {
			test.Errorf("Validate(%q) = nil, want an error", testCase.path)
		}
	}
}
