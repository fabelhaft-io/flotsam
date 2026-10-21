package github

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fabelhaft-io/flotsam/pkg/provider"
	"github.com/fabelhaft-io/flotsam/pkg/provider/providertest"
)

// spec is the provider as Register sees it, for providertest.Backup.
var spec = provider.Spec[Config]{Summary: "GitHub", Hosts: []string{"github.com"}, New: New}

// route is one answer of the fake API.
type route struct {
	status int
	body   string
	// link is the URL of the next page, if any.
	link string
}

// fakeAPI answers like the GitHub API from a fixed set of routes, keyed by
// path and query without per_page, such as "/repos/acme/tool/issues?state=all".
// Requests for any other route fail the test, so a provider can't silently
// depend on a request the test doesn't cover.
type fakeAPI struct {
	test   *testing.T
	routes map[string]route

	mutex sync.Mutex
	// authorization holds the Authorization header of every request.
	authorization []string
}

// ServeHTTP implements http.Handler.
func (fake *fakeAPI) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	fake.mutex.Lock()
	fake.authorization = append(fake.authorization, request.Header.Get("Authorization"))
	fake.mutex.Unlock()

	query := request.URL.Query()
	query.Del("per_page")
	key := request.URL.Path
	// Unescaped, so routes read like the URLs in the provider: "a,b", not "a%2Cb".
	if encoded, _ := url.QueryUnescape(query.Encode()); encoded != "" {
		key += "?" + encoded
	}
	answer, found := fake.routes[key]
	if !found {
		fake.test.Errorf("unexpected request: %s", key)
		http.NotFound(response, request)
		return
	}
	if answer.link != "" {
		response.Header().Set("Link", "<"+answer.link+`>; rel="next", <https://api.github.com/last>; rel="last"`)
	}
	if answer.status != 0 {
		response.WriteHeader(answer.status)
	}
	_, _ = response.Write([]byte(answer.body))
}

// newFakeAPI returns a fake API with routes plus empty lists for every kind of
// item of each repository in repositories, so tests only spell out what they
// are about.
func newFakeAPI(test *testing.T, routes map[string]route, repositories ...string) *fakeAPI {
	all := make(map[string]route)
	for _, name := range repositories {
		for _, items := range kinds {
			all["/repos/"+name+"/"+items.endpoint] = route{body: "[]"}
		}
	}
	for key, answer := range routes {
		all[key] = answer
	}
	return &fakeAPI{test: test, routes: all}
}

// gitRepository creates a local git repository, with one commit unless empty
// is true, and returns its path. A local path works as a clone URL, so git
// runs for real without network access. Tests skip if git isn't installed.
func gitRepository(test *testing.T, empty bool) string {
	test.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		test.Skip("git is not installed")
	}
	directory := test.TempDir()
	runGitForTest(test, directory, "init", "--quiet")
	if !empty {
		// Explicit settings, so the developer's git config (signing, hooks)
		// can't break the test.
		runGitForTest(test, directory, "-c", "user.name=test", "-c", "user.email=test@example.com",
			"-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "--no-verify", "-m", "first")
	}
	return directory
}

// runGitForTest runs git in directory and fails the test on any error.
func runGitForTest(test *testing.T, directory string, arguments ...string) string {
	test.Helper()
	output, err := exec.CommandContext(test.Context(), "git", append([]string{"-C", directory}, arguments...)...).CombinedOutput() //nolint:gosec // test-only git commands with fixed arguments
	if err != nil {
		test.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(output)
}

// repositoryJSON returns a repository object as the API lists it.
func repositoryJSON(test *testing.T, fullName, cloneURL string) string {
	test.Helper()
	data, err := json.Marshal(map[string]any{"full_name": fullName, "clone_url": cloneURL, "has_wiki": false, "private": true})
	if err != nil {
		test.Fatal(err)
	}
	return string(data)
}

func TestNew(test *testing.T) {
	if _, err := New(Config{}, nil); err == nil || !strings.Contains(err.Error(), "set a token") {
		test.Errorf("New() without token and targets = %v, want an error asking for a token", err)
	}
	_, err := New(Config{Targets: []string{
		"https://github.com/acme/tool/issues/42",
		"https://gitlab.com/acme",
		"https://github.com/",
		"acme/tool",
	}}, providertest.NewEnv(test, http.NotFoundHandler()))
	if err == nil {
		test.Fatal("New() with invalid targets succeeded")
	}
	// Every invalid target is reported at once.
	for _, want := range []string{"issues/42", "gitlab.com", `"https://github.com/"`, `"acme/tool"`} {
		if !strings.Contains(err.Error(), want) {
			test.Errorf("error = %q\nwant it to name %q", err, want)
		}
	}
	created, err := New(Config{Targets: []string{"https://github.com/acme", "https://github.com/acme/tool.git/"}}, providertest.NewEnv(test, http.NotFoundHandler()))
	if err != nil {
		test.Fatal(err)
	}
	if targets := created.(*GitHub).targets; targets[0].repository != "" || targets[1].repository != "tool" {
		test.Errorf("targets = %+v", targets)
	}
}

func TestBackupOfOneRepository(test *testing.T) {
	clone := gitRepository(test, false)
	fake := newFakeAPI(test, map[string]route{
		"/repos/acme/tool": {body: repositoryJSON(test, "acme/tool", clone)},
		"/repos/acme/tool/issues?state=all": {
			body: `[{"number": 1, "title": "first"}]`,
			link: "https://api.github.com/repos/acme/tool/issues?page=2&per_page=100&state=all",
		},
		"/repos/acme/tool/issues?page=2&state=all": {body: `[{"number": 2, "title": "second"}]`},
		"/repos/acme/tool/issues/comments":         {body: `[{"id": 10, "issue_url": "https://api.github.com/repos/acme/tool/issues/1"}]`},
		"/repos/acme/tool/pulls?state=all":         {status: http.StatusForbidden, body: `{"message": "Resource not accessible by personal access token"}`},
		"/repos/acme/tool/pulls/comments":          {status: http.StatusForbidden, body: `{"message": "Resource not accessible by personal access token"}`},
		"/repos/acme/tool/releases":                {body: `[{"id": 5, "tag_name": "v1/beta"}]`},
	}, "acme/tool")

	snapshot, err := providertest.Backup(test, spec, Config{Token: "test-token", Targets: []string{"https://github.com/acme/tool"}}, fake)
	if err != nil {
		test.Fatal(err)
	}

	want := []string{
		"acme/tool/git.bundle",
		"acme/tool/issues/1.json",
		"acme/tool/issues/1/comments/10.json",
		"acme/tool/issues/2.json",
		"acme/tool/releases/5.json",
		"acme/tool/repository.json",
	}
	if got := snapshot.Paths(); !slices.Equal(got, want) {
		test.Errorf("paths = %v\nwant    %v", got, want)
	}
	warnings := snapshot.Warnings()
	if len(warnings) != 2 || !strings.Contains(warnings[0], "pull requests skipped: 403 Forbidden: Resource not accessible") ||
		!strings.Contains(warnings[0], "read access to Pull requests") {
		test.Errorf("warnings = %q, want one for pull requests and one for review comments", warnings)
	}
	for _, header := range fake.authorization {
		if header != "Bearer test-token" {
			test.Errorf("Authorization = %q, want the token on every request", header)
		}
	}

	// The bundle is a real git bundle that git can read back.
	bundle, _ := snapshot.File("acme/tool/git.bundle")
	path := filepath.Join(test.TempDir(), "git.bundle")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		test.Fatal(err)
	}
	if heads := runGitForTest(test, ".", "bundle", "list-heads", path); !strings.Contains(heads, "refs/heads/") {
		test.Errorf("bundle heads = %q, want a branch", heads)
	}
}

func TestDisabledIssuesAndEmptyRepositoryAreNotWarnings(test *testing.T) {
	clone := gitRepository(test, true)
	gone := route{status: http.StatusGone, body: `{"message": "Issues are disabled for this repo"}`}
	fake := newFakeAPI(test, map[string]route{
		"/repos/acme/new":                  {body: repositoryJSON(test, "acme/new", clone)},
		"/repos/acme/new/issues?state=all": gone,
		"/repos/acme/new/issues/comments":  gone,
	}, "acme/new")

	snapshot, err := providertest.Backup(test, spec, Config{Targets: []string{"https://github.com/acme/new"}}, fake)
	if err != nil {
		test.Fatal(err)
	}
	if got := snapshot.Paths(); !slices.Equal(got, []string{"acme/new/repository.json"}) {
		test.Errorf("paths = %v, want only repository.json", got)
	}
	if warnings := snapshot.Warnings(); len(warnings) != 0 {
		test.Errorf("warnings = %q, want none", warnings)
	}
	// Without a token, no Authorization header is sent at all.
	for _, header := range fake.authorization {
		if header != "" {
			test.Errorf("Authorization = %q without a token", header)
		}
	}
}

func TestOwnerAndEverythingTargets(test *testing.T) {
	clone := gitRepository(test, false)
	listing := "[" + repositoryJSON(test, "acme/tool", clone) + "]"
	tests := []struct {
		name    string
		targets []string
		routes  map[string]route
	}{
		{
			name: "organization, plus one of its repositories again",
			// The same repository through two targets is backed up once.
			targets: []string{"https://github.com/acme", "https://github.com/Acme/Tool"},
			routes: map[string]route{
				"/users/acme":               {body: `{"type": "Organization"}`},
				"/orgs/acme/repos?type=all": {body: listing},
				"/repos/Acme/Tool":          {body: repositoryJSON(test, "Acme/Tool", clone)},
			},
		},
		{
			name:    "user",
			targets: []string{"https://github.com/acme"},
			routes: map[string]route{
				"/users/acme":                  {body: `{"type": "User"}`},
				"/users/acme/repos?type=owner": {body: listing},
			},
		},
		{
			name: "everything the token can access",
			routes: map[string]route{
				"/user/repos?affiliation=owner,collaborator,organization_member": {body: listing},
			},
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			fake := newFakeAPI(test, testCase.routes, "acme/tool")
			snapshot, err := providertest.Backup(test, spec, Config{Token: "test-token", Targets: testCase.targets}, fake)
			if err != nil {
				test.Fatal(err)
			}
			if got := snapshot.Paths(); !slices.Equal(got, []string{"acme/tool/git.bundle", "acme/tool/repository.json"}) {
				test.Errorf("paths = %v", got)
			}
			if warnings := snapshot.Warnings(); len(warnings) != 0 {
				test.Errorf("warnings = %q, want none", warnings)
			}
		})
	}
}

func TestTargetFailures(test *testing.T) {
	tests := []struct {
		name   string
		answer route
		want   string
	}{
		{name: "not found", answer: route{status: http.StatusNotFound, body: `{"message": "Not Found"}`}, want: "https://github.com/acme/missing: not found, or the token can't see it"},
		{name: "bad token", answer: route{status: http.StatusUnauthorized, body: `{"message": "Bad credentials"}`}, want: "GitHub rejected the token: 401 Unauthorized: Bad credentials"},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			fake := newFakeAPI(test, map[string]route{"/repos/acme/missing": testCase.answer})
			_, err := providertest.Backup(test, spec, Config{Token: "test-token", Targets: []string{"https://github.com/acme/missing"}}, fake)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				test.Errorf("Backup() = %v, want an error containing %q", err, testCase.want)
			}
		})
	}
}

func TestNextPageMustStayOnTheAPI(test *testing.T) {
	header := http.Header{"Link": []string{`<https://attacker.example/steal?page=2>; rel="next"`}}
	if _, err := nextPage(header); err == nil {
		test.Error("nextPage() followed a link to another host, which would send it the token")
	}
	if next, err := nextPage(http.Header{}); next != "" || err != nil {
		test.Errorf("nextPage() without Link = %q, %v; want the last page", next, err)
	}
}

func TestGitEnvironmentScopesTheTokenToTheCloneHost(test *testing.T) {
	github := &GitHub{token: "test-token"}
	environment := strings.Join(github.gitEnvironment("https://github.com/acme/tool.git"), "\n")
	if !strings.Contains(environment, "GIT_CONFIG_KEY_1=http.https://github.com/.extraHeader") {
		test.Errorf("environment doesn't limit the token header to github.com:\n%s", environment)
	}
	if strings.Contains(environment, "test-token") {
		test.Error("the token appears in plain text instead of as basic authentication")
	}
	withoutToken := strings.Join((&GitHub{}).gitEnvironment("https://github.com/acme/tool.git"), "\n")
	if strings.Contains(withoutToken, "extraHeader") {
		test.Error("an Authorization header is configured without a token")
	}
}
