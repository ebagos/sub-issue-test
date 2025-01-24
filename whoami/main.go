package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// GraphQLのクエリ構造
var query struct {
	Repository struct {
		Issue struct {
			Title     githubv4.String
			SubIssues struct {
				Edges []struct {
					Node struct {
						Title githubv4.String
						State githubv4.String
					}
				}
			} `graphql:"subIssues(first: 100)"`
		} `graphql:"issue(number: $issueNumber)"`
	} `graphql:"repository(owner: $org, name: $repo)"`
}

func main() {
	godotenv.Load()
	org := os.Getenv("ORG")
	repo := os.Getenv("REPO")
	issueNumberStr := os.Getenv("ISSUE_NO")
	githubToken := os.Getenv("GITHUB_TOKEN")
	// 環境変数が設定されているか確認
	if org == "" || repo == "" || issueNumberStr == "" || githubToken == "" {
		log.Println(org, repo, issueNumberStr, githubToken)
		log.Fatal("環境変数が設定されていません。GITHUB_ORG, GITHUB_REPO, GITHUB_ISSUE_NUMBER, GITHUB_TOKEN を設定してください。")
	}

	// issue番号を文字列から整数に変換
	var issueNumber int
	_, err := fmt.Sscan(issueNumberStr, &issueNumber)
	if err != nil {
		log.Fatalf("issue番号の変換に失敗しました: %v", err)
	}

	// GitHubクライアントを作成
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: githubToken},
	)
	httpClient := oauth2.NewClient(context.Background(), src)
	client := githubv4.NewClient(httpClient)

	// GraphQLクエリの変数を設定
	variables := map[string]interface{}{
		"org":         githubv4.String(org),
		"repo":        githubv4.String(repo),
		"issueNumber": githubv4.Int(issueNumber),
	}

	// クエリを実行
	err = client.Query(context.Background(), &query, variables)
	if err != nil {
		log.Fatalf("GraphQLクエリの実行に失敗しました: %v", err)
	}

	// Issue情報を出力
	issue := query.Repository.Issue
	fmt.Printf("# Issue #%d: %s\n", issueNumber, issue.Title)
	fmt.Println("## Sub-issues:")
	if len(issue.SubIssues.Edges) == 0 {
		fmt.Println("(No sub-issues found)")
		return
	}

	for _, subIssue := range issue.SubIssues.Edges {
		fmt.Printf("- [%s] %s\n", subIssue.Node.State, subIssue.Node.Title)
	}
}
