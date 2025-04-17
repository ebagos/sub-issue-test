package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// RoundTripper をラップして GraphQL-Features ヘッダーを付与
type headerRoundTripper struct {
	rt http.RoundTripper
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("GraphQL-Features", "sub_issues")
	return h.rt.RoundTrip(req)
}

// GraphQL クエリ構造体
type rootCheckQuery struct {
	Repository struct {
		Issue struct {
			ID     githubv4.ID  // ← 追加：この Issue のグローバル ID
			Number githubv4.Int // ← 取得したい Issue 番号
			Parent *struct {
				ID     githubv4.ID
				Number githubv4.Int // ← 親 Issue の番号も取得
			}
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

func main() {
	ctx := context.Background()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Println("環境変数 GITHUB_TOKEN を設定してください")
		return
	}

	owner := githubv4.String("ebagos")
	repo := githubv4.String("sub-issue-test")
	number := githubv4.Int(4) // 調べたい Issue 番号

	// OAuth2 クライアントにヘッダー設定を合成
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(ctx, src)
	httpClient.Transport = headerRoundTripper{rt: httpClient.Transport}

	client := githubv4.NewClient(httpClient)

	var q rootCheckQuery
	variables := map[string]interface{}{
		"owner":  owner,
		"name":   repo,
		"number": number,
	}

	if err := client.Query(ctx, &q, variables); err != nil {
		fmt.Printf("GraphQL クエリ実行エラー: %v\n", err)
		return
	}

	if q.Repository.Issue.Parent == nil {
		// ルート Issue
		fmt.Printf("Issue #%d (ID: %s) はルート Issue です。Issue ファミリーのルートを担います。\n",
			q.Repository.Issue.Number,
			q.Repository.Issue.ID,
		)
	} else {
		// 子 Issue
		fmt.Printf("Issue #%d (ID: %s) は子 Issue です。親は #%d (ID: %s)\n",
			q.Repository.Issue.Number,
			q.Repository.Issue.ID,
			q.Repository.Issue.Parent.Number,
			q.Repository.Issue.Parent.ID,
		)
	}
}
