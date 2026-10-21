// Package github backs up GitHub repositories: their git data and wikis as
// git bundles, and issues, pull requests, comments, releases, labels and
// milestones as the JSON the API returns.
//
// Settings are a token and a list of targets. Without targets, everything the
// token can access is backed up. README.md describes the token permissions and
// the snapshot layout.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/fabelhaft-io/flotsam/pkg/provider"
)

// init registers the provider under the name used in config files and quick
// backups.
func init() {
	provider.Register("github", provider.Spec[Config]{
		Summary: "Repositories (git and wiki), issues, pull requests, releases, labels and milestones",
		Hosts:   []string{"github.com"},
		New:     New,
	})
}

// Config holds the settings of a GitHub source.
type Config struct {
	Token   string   `setting:"token,secret"  help:"Personal access token; required without targets, optional for public repositories"`
	Targets []string `setting:"targets"       help:"https://github.com/<owner> or https://github.com/<owner>/<repository> URLs (default: everything the token can access)"`
}

// apiURL is where every API request goes. GitHub Enterprise Server would
// need a setting for it; that waits until someone needs it.
const apiURL = "https://api.github.com"

// target is one parsed entry of Config.Targets. An empty repository means
// every repository of the owner.
type target struct {
	url        string
	owner      string
	repository string
}

// GitHub is the provider for one source.
type GitHub struct {
	token   string
	targets []target
	api     api
	logger  *slog.Logger
}

// New checks the settings. Targets are parsed here, without any request, so a
// mistyped URL fails before any backup starts.
func New(configuration Config, environment provider.Env) (provider.Provider, error) {
	if configuration.Token == "" && len(configuration.Targets) == 0 {
		return nil, errors.New("set a token to back up everything it can access, or name targets")
	}
	var targets []target
	var problems []error
	for _, text := range configuration.Targets {
		parsed, err := parseTarget(text)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		targets = append(targets, parsed)
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return &GitHub{
		token:   configuration.Token,
		targets: targets,
		api:     api{client: environment.HTTPClient(), token: configuration.Token},
		logger:  environment.Logger(),
	}, nil
}

// parseTarget accepts the URL of an owner or of one repository, as copied from
// the browser. Anything deeper, such as an issue URL, is rejected instead of
// being cut down to its repository, which would back up more than asked for.
func parseTarget(text string) (target, error) {
	invalid := fmt.Errorf("target %q: use https://github.com/<owner> or https://github.com/<owner>/<repository>", text)
	parsed, err := url.Parse(text)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return target{}, invalid
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	switch {
	case len(segments) == 1 && segments[0] != "":
		return target{url: text, owner: segments[0]}, nil
	case len(segments) == 2 && segments[0] != "" && segments[1] != "":
		return target{url: text, owner: segments[0], repository: strings.TrimSuffix(segments[1], ".git")}, nil
	}
	return target{}, invalid
}

// repository is a repository as the API lists it. Only the fields flotsam
// needs are decoded; raw keeps the whole object for repository.json.
type repository struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
	HasWiki  bool   `json:"has_wiki"`
	raw      json.RawMessage
}

// Backup implements provider.Provider. A failure in one repository is a
// warning, so the others are still backed up; only failing to find out which
// repositories to back up fails the whole backup.
func (github *GitHub) Backup(ctx context.Context, snapshot provider.Snapshot) error {
	if github.token == "" {
		github.logger.Warn("no token: GitHub allows only 60 API requests per hour without one, enough for a small public repository")
	}
	repositories, err := github.repositories(ctx)
	if err != nil {
		return err
	}
	for _, current := range repositories {
		if err := github.backupRepository(ctx, snapshot, current); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			snapshot.Warnf("%s: %v", current.FullName, err)
		}
	}
	return nil
}

// repositories returns every repository the targets name, each once.
func (github *GitHub) repositories(ctx context.Context) ([]repository, error) {
	var found []repository
	seen := make(map[string]bool)
	add := func(item json.RawMessage) error {
		var current repository
		if err := json.Unmarshal(item, &current); err != nil {
			return err
		}
		current.raw = item
		// GitHub names are case-insensitive, so "Acme/Tool" and "acme/tool"
		// are the same repository.
		if key := strings.ToLower(current.FullName); !seen[key] {
			seen[key] = true
			found = append(found, current)
		}
		return nil
	}

	if len(github.targets) == 0 {
		return found, github.api.each(ctx, apiURL+"/user/repos?affiliation=owner,collaborator,organization_member", add)
	}
	for _, current := range github.targets {
		if err := github.resolve(ctx, current, add); err != nil {
			if isStatus(err, http.StatusNotFound) {
				// GitHub answers 404 rather than 403 for private repositories
				// the token can't see, so both cases get the same message.
				return nil, fmt.Errorf("%s: not found, or the token can't see it", current.url)
			}
			return nil, fmt.Errorf("%s: %w", current.url, err)
		}
	}
	return found, nil
}

// resolve passes every repository of one target to add.
func (github *GitHub) resolve(ctx context.Context, current target, add func(json.RawMessage) error) error {
	if current.repository != "" {
		var item json.RawMessage
		if err := github.api.get(ctx, apiURL+"/repos/"+current.owner+"/"+current.repository, &item); err != nil {
			return err
		}
		return add(item)
	}
	// Users and organizations list their repositories at different
	// endpoints, and only the organization one includes private ones.
	var owner struct {
		Type string `json:"type"`
	}
	if err := github.api.get(ctx, apiURL+"/users/"+current.owner, &owner); err != nil {
		return err
	}
	if owner.Type == "Organization" {
		return github.api.each(ctx, apiURL+"/orgs/"+current.owner+"/repos?type=all", add)
	}
	return github.api.each(ctx, apiURL+"/users/"+current.owner+"/repos?type=owner", add)
}

// kind is one list endpoint of a repository whose items are saved one file
// each.
type kind struct {
	name string
	// endpoint is relative to the repository's API URL.
	endpoint string
	// permission is what a fine-grained token needs for it, for the warning
	// when access is denied.
	permission string
	// path returns where an item goes, relative to the repository directory.
	path func(item listItem) string
}

// listItem holds the fields of an API item that its path is made of. They are
// numbers or numeric URL endings, so paths are always safe file names, unlike
// names or tag names, which can contain anything.
type listItem struct {
	ID             int64  `json:"id"`
	Number         int64  `json:"number"`
	IssueURL       string `json:"issue_url"`
	PullRequestURL string `json:"pull_request_url"`
}

// lastSegment returns the end of an API URL, such as the issue number of an
// issue URL.
func lastSegment(address string) string {
	return address[strings.LastIndex(address, "/")+1:]
}

// kinds lists what is saved for every repository. Comments are fetched with
// the repository-wide endpoints, one paged request for all of them, instead
// of one request per issue.
var kinds = []kind{
	{name: "issues", endpoint: "issues?state=all", permission: "Issues",
		path: func(item listItem) string { return fmt.Sprintf("issues/%d.json", item.Number) }},
	{name: "issue comments", endpoint: "issues/comments", permission: "Issues",
		path: func(item listItem) string {
			return fmt.Sprintf("issues/%s/comments/%d.json", lastSegment(item.IssueURL), item.ID)
		}},
	{name: "pull requests", endpoint: "pulls?state=all", permission: "Pull requests",
		path: func(item listItem) string { return fmt.Sprintf("pulls/%d.json", item.Number) }},
	{name: "review comments", endpoint: "pulls/comments", permission: "Pull requests",
		path: func(item listItem) string {
			return fmt.Sprintf("pulls/%s/comments/%d.json", lastSegment(item.PullRequestURL), item.ID)
		}},
	{name: "releases", endpoint: "releases", permission: "Contents",
		path: func(item listItem) string { return fmt.Sprintf("releases/%d.json", item.ID) }},
	{name: "labels", endpoint: "labels", permission: "Issues",
		path: func(item listItem) string { return fmt.Sprintf("labels/%d.json", item.ID) }},
	{name: "milestones", endpoint: "milestones?state=all", permission: "Issues",
		path: func(item listItem) string { return fmt.Sprintf("milestones/%d.json", item.Number) }},
}

// backupRepository saves one repository: its metadata, git data, wiki and
// every kind of item. A part that fails is a warning, so the other parts are
// still saved; only an unexpected API error stops the repository.
func (github *GitHub) backupRepository(ctx context.Context, snapshot provider.Snapshot, source repository) error {
	name := source.FullName
	if err := snapshot.WriteJSON(ctx, name+"/repository.json", source.raw); err != nil {
		return err
	}
	if err := github.bundle(ctx, snapshot, source.CloneURL, name+"/git.bundle"); err != nil {
		snapshot.Warnf("%s: git: %v", name, err)
	}
	if source.HasWiki {
		wikiURL := strings.TrimSuffix(source.CloneURL, ".git") + ".wiki.git"
		if err := github.bundle(ctx, snapshot, wikiURL, name+"/wiki.bundle"); err != nil && !errors.Is(err, errNotFound) {
			snapshot.Warnf("%s: wiki: %v", name, err)
		}
	}

	for _, items := range kinds {
		err := github.api.each(ctx, apiURL+"/repos/"+name+"/"+items.endpoint, func(item json.RawMessage) error {
			var fields listItem
			if err := json.Unmarshal(item, &fields); err != nil {
				return err
			}
			return snapshot.WriteJSON(ctx, name+"/"+items.path(fields), item)
		})
		switch {
		case err == nil:
		// 410 Gone means the feature is turned off, such as issues in a
		// repository without issues: nothing is missing from the backup.
		case isStatus(err, http.StatusGone):
		case isStatus(err, http.StatusForbidden), isStatus(err, http.StatusNotFound):
			snapshot.Warnf("%s: %s skipped: %v (a fine-grained token needs read access to %s)",
				name, items.name, err, items.permission)
		default:
			return fmt.Errorf("%s: %w", items.name, err)
		}
	}
	return nil
}
