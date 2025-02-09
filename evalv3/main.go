package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v69/github"
	"github.com/joho/godotenv"
	"golang.org/x/oauth2"
)

type IssueInfo struct {
	Number    int
	Title     string
	State     string
	Body      string
	SubIssues []IssueInfo
	LinkedPRs []PullRequestInfo
	Labels    []string
	Assignees []string
	CreatedAt string
	UpdatedAt string
}

type PullRequestInfo struct {
	Number    int
	Title     string
	State     string
	URL       string
	CreatedAt string
	UpdatedAt string
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	token := os.Getenv("GITHUB_TOKEN")
	org := os.Getenv("ORG")
	repo := os.Getenv("REPO")

	if token == "" || org == "" || repo == "" {
		log.Fatal("Required environment variables are not set")
	}

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	// レート制限ハンドラーの初期化
	rateLimitHandler := NewRateLimitHandler(client)

	// 初期のレート制限状況を確認
	rateLimitHandler.CheckRateLimit(ctx)

	// すべてのIssueを取得
	issues := getAllIssues(ctx, client, rateLimitHandler, org, repo)

	// Issue（PRではない）のみを処理
	for _, issue := range issues {
		if issue != nil && issue.IsPullRequest() == false {
			issueInfo := processIssue(ctx, client, rateLimitHandler, org, repo, issue)
			printIssueInfo(issueInfo, 0)
		}
	}

	// 最終的なレート制限状況を確認
	rateLimitHandler.CheckRateLimit(ctx)
}

func getAllIssues(ctx context.Context, client *github.Client, rateLimitHandler *RateLimitHandler, org, repo string) []*github.Issue {
	var allIssues []*github.Issue
	opts := &github.IssueListByRepoOptions{
		State:     "all",
		Sort:      "created",
		Direction: "desc",
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	for {
		if err := rateLimitHandler.WaitForRateLimit(ctx); err != nil {
			log.Printf("Error waiting for rate limit: %v", err)
			break
		}

		issues, resp, err := client.Issues.ListByRepo(ctx, org, repo, opts)
		if err != nil {
			log.Printf("Error fetching issues page: %v", err)
			time.Sleep(5 * time.Second) // エラー時は少し待機
			continue
		}

		allIssues = append(allIssues, issues...)
		log.Printf("Fetched %d issues so far...", len(allIssues))

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allIssues
}

func processIssue(ctx context.Context, client *github.Client, rateLimitHandler *RateLimitHandler, org, repo string, issue *github.Issue) IssueInfo {
	issueInfo := IssueInfo{
		SubIssues: make([]IssueInfo, 0),
		LinkedPRs: make([]PullRequestInfo, 0),
		Labels:    make([]string, 0),
		Assignees: make([]string, 0),
	}

	if issue.Number != nil {
		issueInfo.Number = *issue.Number
	}
	if issue.Title != nil {
		issueInfo.Title = *issue.Title
	}
	if issue.State != nil {
		issueInfo.State = *issue.State
	}
	if issue.Body != nil {
		issueInfo.Body = *issue.Body
	}
	if issue.CreatedAt != nil {
		issueInfo.CreatedAt = issue.CreatedAt.String()
	}
	if issue.UpdatedAt != nil {
		issueInfo.UpdatedAt = issue.UpdatedAt.String()
	}

	if issue.Labels != nil {
		for _, label := range issue.Labels {
			if label.Name != nil {
				issueInfo.Labels = append(issueInfo.Labels, *label.Name)
			}
		}
	}

	if issue.Assignees != nil {
		for _, assignee := range issue.Assignees {
			if assignee.Login != nil {
				issueInfo.Assignees = append(issueInfo.Assignees, *assignee.Login)
			}
		}
	}

	if issue.Body != nil {
		subIssues := findSubIssues(ctx, client, rateLimitHandler, org, repo, *issue.Body)
		issueInfo.SubIssues = subIssues
	}

	if issue.Number != nil {
		linkedPRs := findLinkedPRs(ctx, client, rateLimitHandler, org, repo, *issue.Number)
		issueInfo.LinkedPRs = linkedPRs
	}

	return issueInfo
}

func findSubIssues(ctx context.Context, client *github.Client, rateLimitHandler *RateLimitHandler, org, repo, body string) []IssueInfo {
	subIssues := make([]IssueInfo, 0)

	patterns := []string{
		`#(\d+)`,                // #123
		`(?i)related to #(\d+)`, // Related to #123
		`(?i)depends on #(\d+)`, // Depends on #123
		`(?i)blocked by #(\d+)`, // Blocked by #123
		`(?i)parent of #(\d+)`,  // Parent of #123
		`(?i)child of #(\d+)`,   // Child of #123
	}

	processedIssues := make(map[int]bool)

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(body, -1)

		for _, match := range matches {
			if len(match) > 1 {
				var issueNumber int
				_, err := fmt.Sscanf(match[1], "%d", &issueNumber)
				if err == nil && !processedIssues[issueNumber] {
					if err := rateLimitHandler.WaitForRateLimit(ctx); err != nil {
						log.Printf("Error waiting for rate limit: %v", err)
						continue
					}

					issue, _, err := client.Issues.Get(ctx, org, repo, issueNumber)
					if err != nil {
						log.Printf("Error getting issue #%d: %v", issueNumber, err)
						continue
					}

					if issue != nil && !issue.IsPullRequest() {
						subIssue := processIssue(ctx, client, rateLimitHandler, org, repo, issue)
						subIssues = append(subIssues, subIssue)
						processedIssues[issueNumber] = true
					}
				}
			}
		}
	}

	return subIssues
}

func findLinkedPRs(ctx context.Context, client *github.Client, rateLimitHandler *RateLimitHandler, org, repo string, issueNumber int) []PullRequestInfo {
	var linkedPRs []PullRequestInfo
	processedPRs := make(map[int]bool)

	// Search APIを使用してIssueに関連するPRを検索
	searchQuery := fmt.Sprintf("repo:%s/%s type:pr %d", org, repo, issueNumber)
	opts := &github.SearchOptions{
		Sort:  "created",
		Order: "desc",
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	for {
		if err := rateLimitHandler.WaitForRateLimit(ctx); err != nil {
			log.Printf("Error waiting for rate limit: %v", err)
			break
		}

		result, resp, err := client.Search.Issues(ctx, searchQuery, opts)
		if err != nil {
			log.Printf("Error searching PRs: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, item := range result.Issues {
			if item.PullRequestLinks != nil && !processedPRs[*item.Number] {
				if err := rateLimitHandler.WaitForRateLimit(ctx); err != nil {
					log.Printf("Error waiting for rate limit: %v", err)
					continue
				}

				pr, _, err := client.PullRequests.Get(ctx, org, repo, *item.Number)
				if err != nil {
					log.Printf("Error getting PR #%d: %v", *item.Number, err)
					continue
				}

				if pr != nil {
					prInfo := createPRInfo(pr)
					linkedPRs = append(linkedPRs, prInfo)
					processedPRs[*item.Number] = true
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// 追加で本文で参照されているPRも検索
	searchQuery = fmt.Sprintf("repo:%s/%s type:pr body:\"#%d\"", org, repo, issueNumber)
	opts.Page = 1

	for {
		if err := rateLimitHandler.WaitForRateLimit(ctx); err != nil {
			log.Printf("Error waiting for rate limit: %v", err)
			break
		}

		result, resp, err := client.Search.Issues(ctx, searchQuery, opts)
		if err != nil {
			log.Printf("Error searching PRs by body reference: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, item := range result.Issues {
			if item.PullRequestLinks != nil && !processedPRs[*item.Number] {
				if err := rateLimitHandler.WaitForRateLimit(ctx); err != nil {
					log.Printf("Error waiting for rate limit: %v", err)
					continue
				}

				pr, _, err := client.PullRequests.Get(ctx, org, repo, *item.Number)
				if err != nil {
					log.Printf("Error getting PR #%d: %v", *item.Number, err)
					continue
				}

				if pr != nil {
					prInfo := createPRInfo(pr)
					linkedPRs = append(linkedPRs, prInfo)
					processedPRs[*item.Number] = true
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return linkedPRs
}

func createPRInfo(pr *github.PullRequest) PullRequestInfo {
	prInfo := PullRequestInfo{}

	if pr.Number != nil {
		prInfo.Number = *pr.Number
	}
	if pr.Title != nil {
		prInfo.Title = *pr.Title
	}
	if pr.State != nil {
		prInfo.State = *pr.State
	}
	if pr.HTMLURL != nil {
		prInfo.URL = *pr.HTMLURL
	}
	if pr.CreatedAt != nil {
		prInfo.CreatedAt = pr.CreatedAt.String()
	}
	if pr.UpdatedAt != nil {
		prInfo.UpdatedAt = pr.UpdatedAt.String()
	}

	return prInfo
}

func printIssueInfo(issue IssueInfo, indent int) {
	indentStr := strings.Repeat("  ", indent)

	fmt.Printf("%sIssue #%d: %s\n", indentStr, issue.Number, issue.Title)
	fmt.Printf("%s状態: %s\n", indentStr, issue.State)
	if len(issue.Labels) > 0 {
		fmt.Printf("%sラベル: %s\n", indentStr, strings.Join(issue.Labels, ", "))
	}
	if len(issue.Assignees) > 0 {
		fmt.Printf("%sアサイン: %s\n", indentStr, strings.Join(issue.Assignees, ", "))
	}
	if issue.CreatedAt != "" {
		fmt.Printf("%s作成日時: %s\n", indentStr, issue.CreatedAt)
	}
	if issue.UpdatedAt != "" {
		fmt.Printf("%s更新日時: %s\n", indentStr, issue.UpdatedAt)
	}

	if len(issue.LinkedPRs) > 0 {
		fmt.Printf("%s関連PR:\n", indentStr)
		for _, pr := range issue.LinkedPRs {
			fmt.Printf("%s  - #%d: %s (%s)\n", indentStr, pr.Number, pr.Title, pr.State)
			fmt.Printf("%s    URL: %s\n", indentStr, pr.URL)
		}
	}

	if len(issue.SubIssues) > 0 {
		fmt.Printf("%sサブIssue:\n", indentStr)
		for _, subIssue := range issue.SubIssues {
			printIssueInfo(subIssue, indent+1)
		}
	}

	fmt.Println()
}
