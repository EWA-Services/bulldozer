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

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v84/github"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/stretchr/testify/require"
)

func TestCheckRunWithoutPayloadPullRequestsUsesCommitAssociationBeforeBroadList(t *testing.T) {
	const (
		owner = "owner"
		repo  = "repo"
		sha   = "head-sha"
	)

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case fmt.Sprintf("/repos/%s/%s/commits/%s/pulls", owner, repo, sha):
			require.Equal(t, "100", r.URL.Query().Get("per_page"))
			_, err := fmt.Fprint(w, `[]`)
			require.NoError(t, err)
		case fmt.Sprintf("/repos/%s/%s/pulls", owner, repo):
			require.Equal(t, "open", r.URL.Query().Get("state"))
			require.Equal(t, "100", r.URL.Query().Get("per_page"))
			_, err := fmt.Fprint(w, `[]`)
			require.NoError(t, err)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := github.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	require.NoError(t, err)
	client.BaseURL = baseURL

	payload, err := json.Marshal(&github.CheckRunEvent{
		Action: github.String("completed"),
		CheckRun: &github.CheckRun{
			HeadSHA: github.String(sha),
		},
		Installation: &github.Installation{ID: github.Int64(123)},
		Repo: &github.Repository{
			Name: github.String(repo),
			Owner: &github.User{
				Login: github.String(owner),
			},
		},
	})
	require.NoError(t, err)

	handler := &CheckRun{Base: Base{ClientCreator: &testClientCreator{client: client}}}
	require.NoError(t, handler.Handle(context.Background(), "check_run", "delivery-id", payload))

	require.Equal(t, []string{
		"/repos/owner/repo/commits/head-sha/pulls?per_page=100",
		"/repos/owner/repo/pulls?per_page=100&state=open",
	}, requests)
}

type testClientCreator struct {
	githubapp.ClientCreator
	client *github.Client
}

func (c *testClientCreator) NewInstallationClient(installationID int64) (*github.Client, error) {
	return c.client, nil
}

var _ githubapp.ClientCreator = &testClientCreator{}
