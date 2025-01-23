package main

import (
	"context"
	"fmt"
	"os"

	"github.com/shurcooL/githubv4" // GitHub V4 APIクライアントライブラリ
	"golang.org/x/oauth2"          // OAuth2認証用ライブラリ
)

// GraphQLクエリを定義する構造体
type query struct {
	Node struct {
		Issue struct {
			Title  githubv4.String
			Parent struct {
				Issue struct {
					Number githubv4.Int
					Title  githubv4.String
				}
			} `graphql:"parent"` // 親issueの情報を取得するためのフィールド
		} `graphql:"... on Issue"` // Issue型に限定
	} `graphql:"node(id: $issueID)"` // 指定したIDのノードを取得
}

func main() {
	// GitHubアクセストークンを設定
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")}, // 環境変数からアクセストークンを取得
	)
	httpClient := oauth2.NewClient(context.Background(), src)

	// GitHub V4 APIクライアントを作成
	client := githubv4.NewClient(httpClient)

	// sub-issueのIDを設定
	var issueID githubv4.ID
	fmt.Print("Sub-issue ID: ")
	fmt.Scanln(&issueID)

	// クエリを実行
	var q query
	err := client.Query(context.Background(), &q, mapinterface{}{
		"issueID": issueID, // クエリ変数にsub-issueのIDを設定
	})
	if err != nil {
		panic(err)
	}

	// 親issueの情報を表示
	fmt.Println("Parent Issue:")
	fmt.Println("  Number:", q.Node.Issue.Parent.Issue.Number)
	fmt.Println("  Title:", q.Node.Issue.Parent.Issue.Title)
}