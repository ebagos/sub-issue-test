package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

type Issue struct {
	Number    githubv4.Int
	Title     githubv4.String
	CreatedAt githubv4.DateTime
	ClosedAt  *githubv4.DateTime
	State     githubv4.String
	Author    struct {
		Login githubv4.String
	}
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
			}
		}
	} `graphql:"subIssues(first: 100)"`
	TimelineItems struct {
		Nodes []struct {
			TypeName             string `graphql:"__typename"`
			CrossReferencedEvent struct {
				Source struct {
					PullRequest struct {
						Number    githubv4.Int
						Title     githubv4.String
						State     githubv4.String
						Merged    githubv4.Boolean
						CreatedAt githubv4.DateTime
						ClosedAt  *githubv4.DateTime
						Author    struct {
							Login githubv4.String
						}
					} `graphql:"... on PullRequest"`
				}
			} `graphql:"... on CrossReferencedEvent"`
		}
	} `graphql:"timelineItems(first: 100, itemTypes: [CROSS_REFERENCED_EVENT])"`
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

func main() {
	godotenv.Load()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		log.Fatal("GITHUB_TOKEN environment variable is required")
	}
	org := os.Getenv("ORG")
	repo := os.Getenv("REPO")

	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	httpClient := oauth2.NewClient(context.Background(), src)
	client := githubv4.NewClient(httpClient)

	variables := map[string]interface{}{
		"owner":  githubv4.String(org),
		"name":   githubv4.String(repo),
		"cursor": (*githubv4.String)(nil),
	}

	for {
		var q query
		err := client.Query(context.Background(), &q, variables)
		if err != nil {
			log.Fatal(err)
		}

		for _, issue := range q.Repository.Issues.Nodes {
			issueData := struct {
				Number    int        `json:"number"`
				Title     string     `json:"title"`
				CreatedAt time.Time  `json:"created_at"`
				ClosedAt  *time.Time `json:"closed_at,omitempty"`
				State     string     `json:"state"`
				Author    string     `json:"author"`
				Assignees []string   `json:"assignees"`
				SubIssues []struct {
					Number int    `json:"number"`
					Title  string `json:"title"`
					State  string `json:"state"`
				} `json:"sub_issues"`
				LinkedPullRequests []struct {
					Number    int        `json:"number"`
					Title     string     `json:"title"`
					State     string     `json:"state"`
					Merged    bool       `json:"merged"`
					CreatedAt time.Time  `json:"created_at"`
					ClosedAt  *time.Time `json:"closed_at,omitempty"`
					Author    string     `json:"author"`
				} `json:"linked_pull_requests"`
			}{
				Number:    int(issue.Number),
				Title:     string(issue.Title),
				CreatedAt: issue.CreatedAt.Time,
				State:     string(issue.State),
				Author:    string(issue.Author.Login),
			}

			if issue.ClosedAt != nil {
				closedAt := issue.ClosedAt.Time
				issueData.ClosedAt = &closedAt
			}

			for _, assignee := range issue.Assignees.Nodes {
				issueData.Assignees = append(issueData.Assignees, string(assignee.Login))
			}

			for _, subIssue := range issue.SubIssues.Edges {
				issueData.SubIssues = append(issueData.SubIssues, struct {
					Number int    `json:"number"`
					Title  string `json:"title"`
					State  string `json:"state"`
				}{
					Number: int(subIssue.Node.Number),
					Title:  string(subIssue.Node.Title),
					State:  string(subIssue.Node.State),
				})
			}

			for _, item := range issue.TimelineItems.Nodes {
				if item.TypeName == "CrossReferencedEvent" {
					pr := item.CrossReferencedEvent.Source.PullRequest
					if pr.Number != 0 { // PRが存在する場合のみ追加
						prData := struct {
							Number    int        `json:"number"`
							Title     string     `json:"title"`
							State     string     `json:"state"`
							Merged    bool       `json:"merged"`
							CreatedAt time.Time  `json:"created_at"`
							ClosedAt  *time.Time `json:"closed_at,omitempty"`
							Author    string     `json:"author"`
						}{
							Number:    int(pr.Number),
							Title:     string(pr.Title),
							State:     string(pr.State),
							Merged:    bool(pr.Merged),
							CreatedAt: pr.CreatedAt.Time,
							Author:    string(pr.Author.Login),
						}

						if pr.ClosedAt != nil {
							closedAt := pr.ClosedAt.Time
							prData.ClosedAt = &closedAt
						}

						issueData.LinkedPullRequests = append(issueData.LinkedPullRequests, prData)
					}
				}
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
