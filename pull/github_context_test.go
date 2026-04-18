// Copyright 2026 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pull

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v84/github"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/rcrowley/go-metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGithubContextRequiredStatusesUsesRulesetsFirst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/rules/branches/main":
			w.Header().Set("Content-Type", "application/json")
			mustWriteString(t, w, `[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"repo","ruleset_id":1,"parameters":{"required_status_checks":[{"context":"ci/build"},{"context":"ci/lint"},{"context":"ci/build"}],"strict_required_status_checks_policy":true}}]`)
		case "/repos/owner/repo/branches/main/protection":
			t.Fatalf("branch protection fallback should not be called when rulesets return required checks")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ghc, ctx, registry := newTestGithubContext(t, server)

	statuses, err := ghc.RequiredStatuses(ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{"ci/build", "ci/lint"}, statuses)
	assertCounterCount(t, registry, requiredStatusesSourceRulesetsKey, 1)
	assertCounterCount(t, registry, requiredStatusesTotalRulesetsKey, 2)
	assertCounterCount(t, registry, requiredStatusesSourceBranchProtectionKey, 0)
	assertCounterCount(t, registry, requiredStatusesRulesetsFallbackEmptyKey, 0)
}

func TestGithubContextRequiredStatusesFallsBackOnForbiddenRulesets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/repos/owner/repo/rules/branches/main":
			w.WriteHeader(http.StatusForbidden)
			mustWriteString(t, w, `{"message":"forbidden"}`)
		case "/repos/owner/repo/branches/main/protection":
			mustWriteString(t, w, `{"required_status_checks":{"strict":false,"contexts":["ci/legacy"]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ghc, ctx, registry := newTestGithubContext(t, server)

	statuses, err := ghc.RequiredStatuses(ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{"ci/legacy"}, statuses)
	assertCounterCount(t, registry, requiredStatusesSourceBranchProtectionKey, 1)
	assertCounterCount(t, registry, requiredStatusesTotalBranchProtectionKey, 1)
	assertCounterCount(t, registry, requiredStatusesRulesetsFallbackForbiddenKey, 1)
}

func TestGithubContextRequiredStatusesFallsBackToBranchProtectionChecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/repos/owner/repo/rules/branches/main":
			mustWriteString(t, w, `[]`)
		case "/repos/owner/repo/branches/main/protection":
			mustWriteString(t, w, `{"required_status_checks":{"strict":false,"checks":[{"context":"ci/check"},{"context":"ci/check"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ghc, ctx, registry := newTestGithubContext(t, server)

	statuses, err := ghc.RequiredStatuses(ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{"ci/check"}, statuses)
	assertCounterCount(t, registry, requiredStatusesSourceBranchProtectionKey, 1)
	assertCounterCount(t, registry, requiredStatusesTotalBranchProtectionKey, 1)
	assertCounterCount(t, registry, requiredStatusesRulesetsFallbackEmptyKey, 1)
}

func TestGithubContextRequiredStatusesRecordsNoChecksWhenBothSourcesEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/repos/owner/repo/rules/branches/main":
			w.WriteHeader(http.StatusNotFound)
			mustWriteString(t, w, `{"message":"not found"}`)
		case "/repos/owner/repo/branches/main/protection":
			mustWriteString(t, w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ghc, ctx, registry := newTestGithubContext(t, server)

	statuses, err := ghc.RequiredStatuses(ctx)

	require.NoError(t, err)
	assert.Nil(t, statuses)
	assertCounterCount(t, registry, requiredStatusesSourceNoneKey, 1)
	assertCounterCount(t, registry, requiredStatusesRulesetsFallbackNotFoundKey, 1)
}

func newTestGithubContext(t *testing.T, server *httptest.Server) (*GithubContext, context.Context, metrics.Registry) {
	t.Helper()

	client := github.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	require.NoError(t, err)
	client.BaseURL = baseURL

	pr := &github.PullRequest{
		Number: github.Int(1),
		Base: &github.PullRequestBranch{
			Ref: github.String("main"),
			Repo: &github.Repository{
				Name:  github.String("repo"),
				Owner: &github.User{Login: github.String("owner")},
			},
		},
		Head: &github.PullRequestBranch{
			SHA: github.String("head-sha"),
		},
	}

	registry := metrics.NewPrefixedRegistry("bulldozer.")
	ctx := baseapp.WithMetricsCtx(context.Background(), registry)

	return NewGithubContext(client, pr).(*GithubContext), ctx, registry
}

func assertCounterCount(t *testing.T, registry metrics.Registry, key string, expected int64) {
	t.Helper()

	metric := registry.Get(key)
	if expected == 0 {
		if metric == nil {
			return
		}
	}

	counter, ok := metric.(metrics.Counter)
	require.Truef(t, ok, "metric %s is not a counter", key)
	assert.Equal(t, expected, counter.Count(), "unexpected counter value for %s", key)
}

func mustWriteString(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()

	_, err := fmt.Fprint(w, body)
	require.NoError(t, err)
}
