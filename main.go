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

type issueQuery struct {
	Repository struct {
		Issue struct {
			Id     githubv4.ID
			Title  githubv4.String
			Parent struct {
				Number githubv4.Int
				Title  githubv4.String
			} `graphql:"parent"`
		} `graphql:"issue(number: $issueNumber)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
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

	var q issueQuery
	variables := map[string]interface{}{
		"owner":       githubv4.String(org),
		"name":        githubv4.String(repo),
		"issueNumber": githubv4.Int(no),
	}

	err = client.Query(context.Background(), &q, variables)
	if err != nil {
		panic(err)
	}

	fmt.Println("Parent Issue:")
	fmt.Println("  Number:", q.Repository.Issue.Parent.Number)
	fmt.Println("  Title:", q.Repository.Issue.Parent.Title)
}
