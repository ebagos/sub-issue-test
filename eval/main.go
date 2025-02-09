package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/go-github/v69/github"
	"github.com/joho/godotenv"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// RateLimitHandler handles API rate limiting for both REST and GraphQL APIs
type RateLimitHandler struct {
	restClient    *github.Client
	graphqlClient *githubv4.Client
}

func NewRateLimitHandler(restClient *github.Client, graphqlClient *githubv4.Client) *RateLimitHandler {
	return &RateLimitHandler{
		restClient:    restClient,
		graphqlClient: graphqlClient,
	}
}

func (h *RateLimitHandler) WaitForRestRateLimit(ctx context.Context) error {
	rate, _, err := h.restClient.RateLimits(ctx)
	if err != nil {
		return fmt.Errorf("error getting rate limit: %v", err)
	}

	if rate.Core.Remaining == 0 {
		waitDuration := time.Until(rate.Core.Reset.Time)
		log.Printf("Rate limit exceeded. Waiting for %v", waitDuration)
		time.Sleep(waitDuration)
	}

	return nil
}

func (h *RateLimitHandler) WaitForGraphQLRateLimit(ctx context.Context) error {
	var query struct {
		RateLimit struct {
			Remaining int
			ResetAt   githubv4.DateTime
		}
	}

	err := h.graphqlClient.Query(ctx, &query, nil)
	if err != nil {
		return fmt.Errorf("error getting GraphQL rate limit: %v", err)
	}

	if query.RateLimit.Remaining == 0 {
		waitDuration := time.Until(query.RateLimit.ResetAt.Time)
		log.Printf("GraphQL rate limit exceeded. Waiting for %v", waitDuration)
		time.Sleep(waitDuration)
	}

	return nil
}

type Issue struct {
	Number    githubv4.Int
	Title     githubv4.String
	CreatedAt githubv4.DateTime
	ClosedAt  *githubv4.DateTime
	State     githubv4.String
	Author    struct {
		Login githubv4.String
	}
	Labels struct {
		Nodes []struct {
			Name  githubv4.String
			Color githubv4.String
		}
	} `graphql:"labels(first: 10)"`
	Assignees struct {
		Nodes []struct {
			Login githubv4.String
		}
	} `graphql:"assignees(first: 10)"`
	SubIssues struct {
		Edges []struct {
			Node struct {
				Number githubv4.Int
				Title  githubv4.String
				State  githubv4.String
				Labels struct {
					Nodes []struct {
						Name  githubv4.String
						Color githubv4.String
					}
				} `graphql:"labels(first: 10)"`
			}
		}
	} `graphql:"subIssues(first: 100)"`
}

type query struct {
	Repository struct {
		Issues struct {
			Nodes    []Issue
			PageInfo struct {
				EndCursor   githubv4.String
				HasNextPage bool
			}
		} `graphql:"issues(first: 100, after: $cursor)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Merged    bool      `json:"merged"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func findLinkedPRs(ctx context.Context, client *github.Client, rateLimit *RateLimitHandler, org, repo string, issueNumber int) ([]PullRequest, error) {
	var linkedPRs []PullRequest
	processedPRs := make(map[int]bool)

	// Search for PRs that reference this issue
	searchQuery := fmt.Sprintf("repo:%s/%s type:pr %d", org, repo, issueNumber)
	opts := &github.SearchOptions{
		Sort:  "created",
		Order: "desc",
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	for {
		if err := rateLimit.WaitForRestRateLimit(ctx); err != nil {
			return nil, fmt.Errorf("rate limit error: %v", err)
		}

		result, resp, err := client.Search.Issues(ctx, searchQuery, opts)
		if err != nil {
			log.Printf("Error searching PRs: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, item := range result.Issues {
			if item.PullRequestLinks != nil && !processedPRs[*item.Number] {
				if err := rateLimit.WaitForRestRateLimit(ctx); err != nil {
					log.Printf("Rate limit error getting PR #%d: %v", *item.Number, err)
					continue
				}

				pr, _, err := client.PullRequests.Get(ctx, org, repo, *item.Number)
				if err != nil {
					log.Printf("Error getting PR #%d: %v", *item.Number, err)
					continue
				}

				if pr != nil {
					linkedPRs = append(linkedPRs, PullRequest{
						Number:    *pr.Number,
						Title:     *pr.Title,
						State:     *pr.State,
						Merged:    *pr.Merged,
						URL:       *pr.HTMLURL,
						CreatedAt: pr.CreatedAt.Time,
						UpdatedAt: pr.UpdatedAt.Time,
					})
					processedPRs[*pr.Number] = true
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Search for PRs that mention the issue in their body
	searchQuery = fmt.Sprintf("repo:%s/%s type:pr body:\"#%d\"", org, repo, issueNumber)
	opts.Page = 1

	for {
		if err := rateLimit.WaitForRestRateLimit(ctx); err != nil {
			return nil, fmt.Errorf("rate limit error: %v", err)
		}

		result, resp, err := client.Search.Issues(ctx, searchQuery, opts)
		if err != nil {
			log.Printf("Error searching PRs by body reference: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, item := range result.Issues {
			if item.PullRequestLinks != nil && !processedPRs[*item.Number] {
				if err := rateLimit.WaitForRestRateLimit(ctx); err != nil {
					log.Printf("Rate limit error getting PR #%d: %v", *item.Number, err)
					continue
				}

				pr, _, err := client.PullRequests.Get(ctx, org, repo, *item.Number)
				if err != nil {
					log.Printf("Error getting PR #%d: %v", *item.Number, err)
					continue
				}

				if pr != nil {
					linkedPRs = append(linkedPRs, PullRequest{
						Number:    *pr.Number,
						Title:     *pr.Title,
						State:     *pr.State,
						URL:       *pr.HTMLURL,
						CreatedAt: pr.CreatedAt.Time,
						UpdatedAt: pr.UpdatedAt.Time,
					})
					processedPRs[*pr.Number] = true
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return linkedPRs, nil
}

type IssueOutput struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	State     string     `json:"state"`
	Author    string     `json:"author"`
	Labels    []Label    `json:"labels"`
	Assignees []string   `json:"assignees"`
	SubIssues []struct {
		Number int     `json:"number"`
		Title  string  `json:"title"`
		State  string  `json:"state"`
		Labels []Label `json:"labels"`
	} `json:"sub_issues"`
	LinkedPullRequests []PullRequest `json:"linked_pull_requests"`
}

func main() {
	godotenv.Load()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		log.Fatal("GITHUB_TOKEN environment variable is required")
	}
	org := os.Getenv("ORG")
	repo := os.Getenv("REPO")

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)

	restClient := github.NewClient(tc)
	graphqlClient := githubv4.NewClient(tc)
	rateLimitHandler := NewRateLimitHandler(restClient, graphqlClient)

	variables := map[string]interface{}{
		"owner":  githubv4.String(org),
		"name":   githubv4.String(repo),
		"cursor": (*githubv4.String)(nil),
	}

	for {
		if err := rateLimitHandler.WaitForGraphQLRateLimit(ctx); err != nil {
			log.Fatal(err)
		}

		var q query
		err := graphqlClient.Query(context.Background(), &q, variables)
		if err != nil {
			log.Fatal(err)
		}

		for _, issue := range q.Repository.Issues.Nodes {
			issueData := IssueOutput{
				Number:    int(issue.Number),
				Title:     string(issue.Title),
				CreatedAt: issue.CreatedAt.Time,
				State:     string(issue.State),
				Author:    string(issue.Author.Login),
				Labels:    make([]Label, 0),
				Assignees: make([]string, 0),
			}

			if issue.ClosedAt != nil {
				closedAt := issue.ClosedAt.Time
				issueData.ClosedAt = &closedAt
			}

			// ラベル情報の取得
			for _, label := range issue.Labels.Nodes {
				issueData.Labels = append(issueData.Labels, Label{
					Name:  string(label.Name),
					Color: string(label.Color),
				})
			}

			for _, assignee := range issue.Assignees.Nodes {
				issueData.Assignees = append(issueData.Assignees, string(assignee.Login))
			}

			for _, subIssue := range issue.SubIssues.Edges {
				subIssueLabels := make([]Label, 0)
				for _, label := range subIssue.Node.Labels.Nodes {
					subIssueLabels = append(subIssueLabels, Label{
						Name:  string(label.Name),
						Color: string(label.Color),
					})
				}

				issueData.SubIssues = append(issueData.SubIssues, struct {
					Number int     `json:"number"`
					Title  string  `json:"title"`
					State  string  `json:"state"`
					Labels []Label `json:"labels"`
				}{
					Number: int(subIssue.Node.Number),
					Title:  string(subIssue.Node.Title),
					State:  string(subIssue.Node.State),
					Labels: subIssueLabels,
				})
			}

			linkedPRs, err := findLinkedPRs(ctx, restClient, rateLimitHandler, org, repo, int(issue.Number))
			if err != nil {
				log.Printf("Error fetching linked PRs for issue #%d: %v", issue.Number, err)
			} else {
				issueData.LinkedPullRequests = linkedPRs
			}

			output, err := json.MarshalIndent(issueData, "", "  ")
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(string(output))
		}

		if !q.Repository.Issues.PageInfo.HasNextPage {
			break
		}
		variables["cursor"] = githubv4.String(q.Repository.Issues.PageInfo.EndCursor)
	}
}
