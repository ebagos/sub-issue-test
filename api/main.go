package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

type LinkedItem struct {
	Type   string `json:"type"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

type Author struct {
	Login string `json:"login"`
}

type Issue struct {
	CreatedAt   githubv4.DateTime `json:"created_at"`
	ClosedAt    githubv4.DateTime `json:"closed_at"`
	State       string            `json:"state"`
	Author      Author            `json:"author"`
	Body        string            `json:"body"`
	Labels      []Label           `json:"labels"`
	LinkedItems []LinkedItem      `json:"linked_items,omitempty"`
}

type Label struct {
	Name string `json:"name"`
}

type IssueFragment struct {
	Number    githubv4.Int
	Title     githubv4.String
	Url       githubv4.String
	State     githubv4.String
	CreatedAt githubv4.DateTime
	ClosedAt  githubv4.DateTime
	Author    struct {
		Login githubv4.String
	}
	Body   githubv4.String
	Labels struct {
		Nodes []struct {
			Name githubv4.String
		}
	} `graphql:"labels(first: 100)"`
	Parent struct {
		Id     githubv4.ID
		Number githubv4.Int
	} `graphql:"parent"`
}

type topParentQuery struct {
	Repository struct {
		Issue IssueFragment `graphql:"issue(number: $issueNumber)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

func getTopParentIssue(ctx context.Context, client *githubv4.Client, org, repo string, issueNumber int) (*Issue, error) {
	var q topParentQuery
	variables := map[string]interface{}{
		"owner":       githubv4.String(org),
		"name":        githubv4.String(repo),
		"issueNumber": githubv4.Int(issueNumber),
	}

	visitedIssues := make(map[int]bool)

	for {
		err := client.Query(ctx, &q, variables)
		if err != nil {
			return nil, fmt.Errorf("query error: %w", err)
		}

		current := int(q.Repository.Issue.Number)
		if visitedIssues[current] {
			fmt.Printf("Loop detected at issue %d\n", current)
			return convertToIssue(&q.Repository.Issue), nil
		}
		visitedIssues[current] = true

		if q.Repository.Issue.Parent.Number == 0 {
			fmt.Printf("No parent found for issue %d\n", current)
			return convertToIssue(&q.Repository.Issue), nil
		}

		parentNumber := int(q.Repository.Issue.Parent.Number)
		fmt.Printf("Issue: %d -> Parent: %d\n", current, parentNumber)

		if parentNumber <= 0 {
			fmt.Printf("Invalid parent number %d for issue %d\n", parentNumber, current)
			return convertToIssue(&q.Repository.Issue), nil
		}

		variables["issueNumber"] = githubv4.Int(parentNumber)
	}
}

func convertToIssue(gqlIssue *IssueFragment) *Issue {
	labels := make([]Label, len(gqlIssue.Labels.Nodes))
	for i, label := range gqlIssue.Labels.Nodes {
		labels[i] = Label{
			Name: string(label.Name),
		}
	}

	return &Issue{
		CreatedAt: gqlIssue.CreatedAt,
		ClosedAt:  gqlIssue.ClosedAt,
		State:     string(gqlIssue.State),
		Author: Author{
			Login: string(gqlIssue.Author.Login),
		},
		Body:   string(gqlIssue.Body),
		Labels: labels,
		LinkedItems: []LinkedItem{{
			Type:   "Issue",
			Number: int(gqlIssue.Number),
			Title:  string(gqlIssue.Title),
			URL:    string(gqlIssue.Url),
			State:  string(gqlIssue.State),
		}},
	}
}

func main() {
	godotenv.Load()
	org := os.Getenv("ORG")
	repo := os.Getenv("REPO")
	noString := os.Getenv("ISSUE_NO")
	no, err := strconv.Atoi(noString)
	if err != nil {
		panic(err)
	}

	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	)
	httpClient := oauth2.NewClient(context.Background(), src)
	client := githubv4.NewClient(httpClient)

	issue, err := getTopParentIssue(context.Background(), client, org, repo, no)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Top Parent Issue: %+v\n", issue)
}
