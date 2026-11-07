package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/collect/azure"
	"github.com/jbrathwa/outflow/internal/collect/k8s"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/model"
)

// liveDeps are the live-collection constructors; tests replace them with fakes.
type liveDeps struct {
	cluster  func(kubeconfig, context string) (collect.ClusterSource, error)
	tokens   func() (azure.TokenSource, error)
	resolver collect.Resolver
}

var deps = liveDeps{
	cluster: func(kubeconfig, context string) (collect.ClusterSource, error) {
		s, err := k8s.New(kubeconfig, context)
		if err != nil {
			return nil, err
		}
		return s, nil
	},
	tokens:   azure.DefaultTokens,
	resolver: collect.NewDNSResolver(),
}

// signInError marks an Azure credential failure so it gets the sign-in hint.
type signInError struct{ err error }

func (e *signInError) Error() string { return "azure sign-in: " + e.err.Error() }
func (e *signInError) Unwrap() error { return e.err }

// errInterrupted is a run cancelled by Ctrl-C before collection finished.
var errInterrupted = errors.New("interrupted")

// liveMessage renders a live-collection error for "outflow: <msg>".
func liveMessage(err error) string {
	if errors.Is(err, errInterrupted) {
		return errInterrupted.Error()
	}
	var se *signInError
	if errors.As(err, &se) {
		// azidentity errors list every credential tried over many lines; keep the summary.
		first, _, _ := strings.Cut(se.err.Error(), "\n")
		return fmt.Sprintf("azure sign-in: %s (run az login, or set up workload identity in CI)", strings.TrimSpace(first))
	}
	return err.Error()
}

const (
	minJoinWindow = time.Minute
	maxJoinWindow = 60 * time.Minute
)

func parseJoinWindow(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil || d < minJoinWindow || d > maxJoinWindow {
		return 0, fmt.Errorf("--join-window must be between 1m and 60m, got %s", s)
	}
	return d, nil
}

var workspaceRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// live is a configured collector plus its options.
type live struct {
	col collect.Collector
	opt collect.Options
}

// signInTimeout bounds the eager token fetch that checks the Azure sign-in.
const signInTimeout = 30 * time.Second

// newLive validates the live flags, then opens the cluster. Azure tokens are only
// requested once the azure platform is chosen; ctx is the run's (Ctrl-C) context.
func (c *common) newLive(ctx context.Context, cfg config.Config) (*live, error) {
	since, err := collect.ParseSince(c.since)
	if err != nil {
		return nil, fmt.Errorf("--since: %w", err)
	}
	profile := c.profile
	switch profile {
	case "auto", "azure", "generic":
	default:
		return nil, errors.New("--profile must be auto, azure or generic")
	}
	if c.workspace != "" && !workspaceRE.MatchString(c.workspace) {
		return nil, fmt.Errorf("--workspace must be a Log Analytics workspace ID (GUID), got %q", c.workspace)
	}
	laURL, armURL, testEnv, err := azure.TestEndpointsFromEnv()
	if err != nil {
		return nil, err
	}
	sources := cfg.Sources.WithWorkspace(c.workspace)

	k8s.UserAgent = "outflow/" + strings.TrimPrefix(versionString(Version, Commit, Date, moduleVersion()), "outflow ")
	cluster, err := deps.cluster("", c.context)
	if err != nil {
		return nil, err
	}
	platform := func(cl model.Cluster, _ []string) (collect.Platform, error) {
		name := profile
		if name == "auto" {
			name = "generic"
			if cl.Platform == "AKS" {
				name = "azure"
			}
		}
		if name == "generic" {
			return collect.Platform{Name: "generic"}, nil
		}
		var tokens azure.TokenSource = azure.StaticToken("test")
		if !testEnv {
			t, err := deps.tokens()
			if err != nil {
				return collect.Platform{}, &signInError{err}
			}
			tokens = t
		}
		// Building the credential does not sign in; fetch one token now so a missing
		// login fails the run with a hint instead of every Azure source going "missing".
		tctx, cancel := context.WithTimeout(ctx, signInTimeout)
		defer cancel()
		if _, err := tokens.Token(tctx, azure.ScopeLogAnalytics); err != nil {
			if ctx.Err() != nil {
				return collect.Platform{}, errInterrupted
			}
			return collect.Platform{}, &signInError{err}
		}
		return azure.NewPlatform(azure.Config{Sources: sources, Tokens: tokens, LAEndpoint: laURL, ARMEndpoint: armURL}), nil
	}
	return &live{
		col: collect.Collector{Cluster: cluster, Platform: platform, Resolver: deps.resolver},
		opt: collect.Options{Since: since},
	}, nil
}
