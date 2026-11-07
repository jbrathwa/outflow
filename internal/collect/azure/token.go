// Package azure holds the read-only Azure clients: tokens and Log Analytics queries.
package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// Token scopes.
const (
	ScopeLogAnalytics = "https://api.loganalytics.io/.default"
	ScopeARM          = "https://management.azure.com/.default"
)

// TokenSource hands out bearer tokens for a scope.
type TokenSource interface {
	Token(ctx context.Context, scope string) (string, error)
}

// StaticToken is a fixed token, for tests and the OUTFLOW_TEST_* hooks.
type StaticToken string

// Token returns the fixed token.
func (s StaticToken) Token(context.Context, string) (string, error) { return string(s), nil }

type credTokens struct {
	cred *azidentity.DefaultAzureCredential
}

// DefaultTokens uses the default Azure credential chain (env, CLI, managed identity, ...).
func DefaultTokens() (TokenSource, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, err
	}
	return credTokens{cred}, nil
}

func (c credTokens) Token(ctx context.Context, scope string) (string, error) {
	t, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
	if err != nil {
		return "", err
	}
	return t.Token, nil
}
