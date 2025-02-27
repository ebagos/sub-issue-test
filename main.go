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

type parentIssueQuery struct {
    Repository struct {
        Issue struct {
            Id     githubv4.ID
            Title  githubv4.String
            Parent struct {
                Number githubv4.Int
                Title  githubv4.String
                Url    githubv4.String
            } `graphql:"parent"`
        } `graphql:"issue(number: $issueNumber)"`
    } `graphql:"repository(owner: $owner, name: $name)"`
}

type childIssuesQuery struct {
	SubIssues struct {
		NodessubIssue
	} `graphql:"subIssue(first: 10)"`
}

func getParentIssue(client *githubv4.Client, org, repo string, issueNumber int) (*parentIssueQuery, error) {
    var q parentIssueQuery
    variables := map[string]interface{}{
        "owner":       githubv4.String(org),
        "name":        githubv4.String(repo),
        "issueNumber": githubv4.Int(issueNumber),
    }

    err := client.Query(context.Background(), &q, variables)
    if err != nil {
        return nil, err
    }

    return &q, nil
}

func getChildIssues(client *githubv4.Client, org, repo string, issueNumber int) (*childIssuesQuery, error) {
    var q childIssuesQuery
    variables := map[string]interface{}{
        "owner":       githubv4.String(org),
        "name":        githubv4.String(repo),
        "issueNumber": githubv4.Int(issueNumber),
    }

    err := client.Query(context.Background(), &q, variables)
    if err != nil {
        return nil, err
    }

    return &q, nil
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

    // 親Issueの取得
    parentIssue, err := getParentIssue(client, org, repo, no)
    if err != nil {
        panic(err)
    }

    fmt.Println("Parent Issue:")
    fmt.Println("      ID:", parentIssue.Repository.Issue.Parent)
    fmt.Println("  Number:", parentIssue.Repository.Issue.Parent.Number)
    fmt.Println("   Title:", parentIssue.Repository.Issue.Parent.Title)

    // 子Issueの取得
    childIssues, err := getChildIssues(client, org, repo, no)
    if err != nil {
        panic(err)
    }

    fmt.Println("\nChild Issues:")
    for i, child := range childIssues.Repository.Issue.Children.Nodes {
        fmt.Printf("\nChild %d:\n", i+1)
        fmt.Println("  Number:", child.Number)
        fmt.Println("   Title:", child.Title)
        fmt.Println("    URL:", child.Url)
    }
}
