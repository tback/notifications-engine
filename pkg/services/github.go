package services

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	texttemplate "text/template"
	"unicode/utf8"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v41/github"
	log "github.com/sirupsen/logrus"
	giturls "github.com/whilp/git-urls"

	httputil "github.com/argoproj/notifications-engine/pkg/util/http"
	"github.com/argoproj/notifications-engine/pkg/util/text"
)

var (
	gitSuffix = regexp.MustCompile(`\.git$`)
)

type GitHubOptions struct {
	AppID             int64  `json:"appID"`
	InstallationID    int64  `json:"installationID"`
	PrivateKey        string `json:"privateKey"`
	EnterpriseBaseURL string `json:"enterpriseBaseURL"`
}

type GitHubNotification struct {
	State     string `json:"state,omitempty"`
	Label     string `json:"label,omitempty"`
	TargetURL string `json:"targetURL,omitempty"`
	RepoURL   string `json:"repoURL,omitempty"`
	Revision  string `json:"revision,omitempty"`
}

const (
	defaultRepoURLtemplate  = "{{.app.spec.source.repoURL}}"
	defaultRevisionTemplate = "{{.app.status.operationState.syncResult.revision}}"
)

func (g *GitHubNotification) GetTemplater(name string, f texttemplate.FuncMap) (Templater, error) {

	repoURLtemplate := defaultRepoURLtemplate
	if g.RepoURL != "" {
		repoURLtemplate = g.RepoURL
	}
	repoURL, err := texttemplate.New(name).Funcs(f).Parse(repoURLtemplate)
	if err != nil {
		return nil, err
	}

	revisionTemplate := defaultRevisionTemplate
	if g.Revision != "" {
		revisionTemplate = g.Revision
	}
	revision, err := texttemplate.New(name).Funcs(f).Parse(revisionTemplate)
	if err != nil {
		return nil, err
	}

	state, err := texttemplate.New(name).Funcs(f).Parse(g.State)
	if err != nil {
		return nil, err
	}

	label, err := texttemplate.New(name).Funcs(f).Parse(g.Label)
	if err != nil {
		return nil, err
	}

	targetURL, err := texttemplate.New(name).Funcs(f).Parse(g.TargetURL)
	if err != nil {
		return nil, err
	}

	return func(notification *Notification, vars map[string]interface{}) error {
		if notification.GitHub == nil {
			notification.GitHub = &GitHubNotification{}
		}

		var repoData bytes.Buffer
		if err := repoURL.Execute(&repoData, vars); err != nil {
			return err
		}
		notification.GitHub.RepoURL = repoData.String()

		var revisionData bytes.Buffer
		if err := revision.Execute(&revisionData, vars); err != nil {
			return err
		}
		notification.GitHub.Revision = revisionData.String()

		var stateData bytes.Buffer
		if err := state.Execute(&stateData, vars); err != nil {
			return err
		}
		notification.GitHub.State = stateData.String()

		var labelData bytes.Buffer
		if err := label.Execute(&labelData, vars); err != nil {
			return err
		}
		notification.GitHub.Label = labelData.String()

		var targetData bytes.Buffer
		if err := targetURL.Execute(&targetData, vars); err != nil {
			return err
		}
		notification.GitHub.TargetURL = targetData.String()

		return nil
	}, nil
}

func NewGitHubService(opts GitHubOptions) (NotificationService, error) {
	url := "https://api.github.com"
	if opts.EnterpriseBaseURL != "" {
		url = opts.EnterpriseBaseURL
	}

	tr := httputil.NewLoggingRoundTripper(
		httputil.NewTransport(url, false), log.WithField("service", "github"))
	itr, err := ghinstallation.New(tr, opts.AppID, opts.InstallationID, []byte(opts.PrivateKey))
	if err != nil {
		return nil, err
	}

	var client *github.Client
	if opts.EnterpriseBaseURL == "" {
		client = github.NewClient(&http.Client{Transport: itr})
	} else {
		itr.BaseURL = opts.EnterpriseBaseURL
		client, err = github.NewEnterpriseClient(opts.EnterpriseBaseURL, "", &http.Client{Transport: itr})
		if err != nil {
			return nil, err
		}
	}

	return &gitHubService{
		opts:   opts,
		client: client,
	}, nil
}

type gitHubService struct {
	opts GitHubOptions

	client *github.Client
}

func trunc(message string, n int) string {
	if utf8.RuneCountInString(message) > n {
		return string([]rune(message)[0:n-3]) + "..."
	}
	return message
}

func fullNameByRepoURL(rawURL string) string {
	parsed, err := giturls.Parse(rawURL)
	if err != nil {
		panic(err)
	}

	path := gitSuffix.ReplaceAllString(parsed.Path, "")
	if pathParts := text.SplitRemoveEmpty(path, "/"); len(pathParts) >= 2 {
		return strings.Join(pathParts[:2], "/")
	}

	return path
}

func (g gitHubService) Send(notification Notification, _ Destination) error {
	if notification.GitHub == nil {
		return fmt.Errorf("config is empty")
	}

	u := strings.Split(fullNameByRepoURL(notification.GitHub.RepoURL), "/")
	// maximum is 140 characters
	description := trunc(notification.Message, 140)
	_, _, err := g.client.Repositories.CreateStatus(
		context.Background(),
		u[0],
		u[1],
		notification.GitHub.Revision,
		&github.RepoStatus{
			State:       &notification.GitHub.State,
			Description: &description,
			Context:     &notification.GitHub.Label,
			TargetURL:   &notification.GitHub.TargetURL,
		},
	)
	if err != nil {
		return err
	}

	return nil
}
