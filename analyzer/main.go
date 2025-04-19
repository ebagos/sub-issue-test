package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

const (
	estimatedLabel = "見積時間"
	actualLabel    = "実績時間"
	sbiLabel       = "sbi"
	jstOffset      = 9 * 60 * 60 // JSTは UTC+9時間
)

// JSTの定義（パッケージレベルで定義）
var jst = time.FixedZone("JST", jstOffset)

// IssueTimeInfo はIssueの時間情報を格納する構造体
type IssueTimeInfo struct {
	IssueURL      string     `json:"issue_url"`
	Title         string     `json:"title"`
	Author        string     `json:"author"`
	Assignees     []string   `json:"assignees"`
	CreatedAt     time.Time  `json:"created_at"` // nilにならないように値型にする
	ClosedAt      *time.Time `json:"closed_at"`  // 閉じていない場合はnilになるためポインタ
	State         string     `json:"state"`
	StateReason   string     `json:"state_reason"`
	EstimatedTime float64    `json:"estimated_time"`
	ActualTime    float64    `json:"actual_time"`
	Labels        []string   `json:"labels"`
}

// DateRange は日付範囲を表す構造体
type DateRange struct {
	StartDate time.Time
	EndDate   time.Time
}

// WeeklyPeriod は週間期間を表す構造体
type WeeklyPeriod struct {
	StartDate time.Time
	EndDate   time.Time
	Weekday   int
}

// FilterOptions は複数のフィルタリングオプションを格納する構造体
type FilterOptions struct {
	ClosedDateRange     *DateRange    // 閉じられた日付の範囲
	CreatedAfterDate    *time.Time    // 指定日以降に作成された
	IncludeOpenIssues   bool          // 未閉じIssueを含むか
	WeeklyPeriod        *WeeklyPeriod // 週次期間
	RequireSbiLabel     bool          // "sbi"ラベルが必要か
	ExcludeNotPlanned   bool          // "NOT_PLANNED"で閉じられたIssueを除外するか
	AllowedRepositories []string      // 対象リポジトリのリスト
}

// GraphQLクエリ用の型定義
type queryProjectV2 struct {
	Organization struct {
		ProjectV2 struct {
			Title string
			Items struct {
				PageInfo struct {
					HasNextPage bool
					EndCursor   githubv4.String
				}
				Nodes []struct {
					Content struct {
						TypeName string `graphql:"__typename"`
						Issue    struct {
							Number      int
							Title       string
							State       string
							StateReason *string
							Author      struct {
								Login string
							}
							Labels struct {
								Nodes []struct {
									Name string
								}
							} `graphql:"labels(first: 100)"`
							Assignees struct {
								Nodes []struct {
									Login string
								}
							} `graphql:"assignees(first: 10)"`
							URL        string
							Repository struct {
								Name string
							}
							CreatedAt githubv4.DateTime
							ClosedAt  *githubv4.DateTime
						} `graphql:"... on Issue"`
					} `graphql:"content"`
					FieldValues struct {
						Nodes []struct {
							TypeName string `graphql:"__typename"`
							// 数値フィールド用のフラグメント
							NumberField struct {
								Field struct {
									Name string `graphql:"name"`
								} `graphql:"field"`
								Number *float64
							} `graphql:"... on ProjectV2ItemFieldNumberValue"`
							// 日付フィールド用のフラグメント（将来的に必要な場合）
							DateField struct {
								Field struct {
									Name string `graphql:"name"`
								} `graphql:"field"`
								Date githubv4.DateTime
							} `graphql:"... on ProjectV2ItemFieldDateValue"`
							// テキストフィールド用のフラグメント（将来的に必要な場合）
							TextField struct {
								Field struct {
									Name string `graphql:"name"`
								} `graphql:"field"`
								Text string
							} `graphql:"... on ProjectV2ItemFieldTextValue"`
							// シングルセレクトフィールド用のフラグメント（将来的に必要な場合）
							SingleSelectField struct {
								Field struct {
									Name string `graphql:"name"`
								} `graphql:"field"`
								OptionName string `graphql:"name"`
							} `graphql:"... on ProjectV2ItemFieldSingleSelectValue"`
						}
					} `graphql:"fieldValues(first: 100)"`
				}
			} `graphql:"items(first: 100, after: $cursor)"`
		} `graphql:"projectV2(number: $projectNum)"`
	} `graphql:"organization(login: $org)"`
}

// parseJSTDate はJSTタイムゾーンで日付を解析する
func parseJSTDate(dateStr string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", dateStr, jst)
}

// calculateWeeklyPeriod は昨日を含む週の特定曜日からの1週間の期間を計算する
func calculateWeeklyPeriod(weekday int) WeeklyPeriod {
	// 昨日の日時（JST）
	yesterday := time.Now().In(jst).AddDate(0, 0, -1)

	// 昨日が含まれる週の開始曜日を計算
	daysSinceTargetWeekday := (int(yesterday.Weekday()) - weekday + 7) % 7
	lastTargetWeekday := yesterday.AddDate(0, 0, -daysSinceTargetWeekday)

	// 時刻部分をリセットして、その日の00:00:00に設定
	lastTargetWeekday = time.Date(
		lastTargetWeekday.Year(), lastTargetWeekday.Month(), lastTargetWeekday.Day(),
		0, 0, 0, 0, jst)

	// 次の週の同じ曜日(期間の終了日は含まない)
	// 7日後の00:00:00が終了時刻、つまり前日の23:59:59までが対象
	nextWeekSameDay := lastTargetWeekday.AddDate(0, 0, 7)

	return WeeklyPeriod{
		StartDate: lastTargetWeekday,
		EndDate:   nextWeekSameDay,
		Weekday:   weekday,
	}
}

func main() {
	// 環境変数のロード
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using existing environment variables")
	}

	// 必要な環境変数の取得
	org := os.Getenv("ORG")
	if org == "" {
		log.Fatal("ORG environment variable must be set")
	}

	projectStr := os.Getenv("PROJECT")
	if projectStr == "" {
		log.Fatal("PROJECT environment variable must be set")
	}
	projectNum, err := strconv.Atoi(projectStr)
	if err != nil {
		log.Fatalf("Invalid PROJECT number: %v", err)
	}

	reposStr := os.Getenv("REPOS")
	if reposStr == "" {
		log.Fatal("REPOS environment variable must be set")
	}
	repos := strings.Split(reposStr, ",")
	// リポジトリ名をトリム
	for i := range repos {
		repos[i] = strings.TrimSpace(repos[i])
	}

	// フィルターオプションの作成
	filterOptions := FilterOptions{
		IncludeOpenIssues:   false, // デフォルトでは閉じられたIssueのみ対象
		RequireSbiLabel:     true,  // "sbi"ラベルが必要
		ExcludeNotPlanned:   true,  // "NOT_PLANNED"で閉じられたIssueを除外
		AllowedRepositories: repos, // 対象リポジトリ
	}

	// 日付フィルタの取得と解析
	startDateStr := os.Getenv("START_DATE")
	endDateStr := os.Getenv("END_DATE")

	if startDateStr != "" && endDateStr != "" {
		startDate, err := parseJSTDate(startDateStr)
		if err != nil {
			log.Fatalf("Invalid START_DATE format: %v", err)
		}

		endDate, err := parseJSTDate(endDateStr)
		if err != nil {
			log.Fatalf("Invalid END_DATE format: %v", err)
		}
		// 終了日の終わりまでを含めるために23:59:59に設定
		endDate = endDate.Add(24*time.Hour - time.Second)

		filterOptions.ClosedDateRange = &DateRange{
			StartDate: startDate,
			EndDate:   endDate,
		}
	}

	// 新機能1: チェック開始日時の取得
	checkStartDateStr := os.Getenv("CHECK_START_DATE")
	if checkStartDateStr != "" {
		checkStartDate, err := parseJSTDate(checkStartDateStr)
		if err != nil {
			log.Fatalf("Invalid CHECK_START_DATE format: %v", err)
		}
		filterOptions.CreatedAfterDate = &checkStartDate
	}

	// 新機能2: 曜日指定による範囲指定
	weekdayStr := os.Getenv("WEEKDAY")
	if weekdayStr != "" {
		wd, err := strconv.Atoi(weekdayStr)
		if err != nil {
			log.Fatalf("Invalid WEEKDAY format (should be 0-7): %v", err)
		}
		if wd < 0 || wd > 7 {
			log.Fatalf("WEEKDAY should be between 0 and 7 (0/7=Sunday, 1=Monday, ..., 6=Saturday)")
		}
		// 7も日曜として扱う
		if wd == 7 {
			wd = 0
		}

		weeklyPeriod := calculateWeeklyPeriod(wd)
		filterOptions.WeeklyPeriod = &weeklyPeriod
	}

	// GitHubトークンの取得
	token := getGitHubToken()

	// GraphQLクライアントの初期化
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	httpClient := oauth2.NewClient(context.Background(), src)
	client := githubv4.NewClient(httpClient)
	ctx := context.Background()

	// プロジェクトからIssueを取得
	allIssues, err := fetchAllProjectIssues(client, ctx, org, projectNum)
	if err != nil {
		log.Fatalf("Error fetching issues from project: %v", err)
	}

	// フィルタリングを適用
	filteredIssues := filterIssues(allIssues, filterOptions)

	// 結果の出力
	if len(filteredIssues) == 0 {
		fmt.Println("No issues found matching the criteria")
		return
	}

	fmt.Printf("Found %d issues matching criteria in repositories: %s\n\n",
		len(filteredIssues), strings.Join(repos, ", "))

	// サマリー情報を出力
	printSummary(filteredIssues)

	// 月ごとのサマリー
	printMonthlySummary(filteredIssues)

	// 新機能1: 指定された日時以降に作成されたIssueで時間情報が欠けているものを出力
	if filterOptions.CreatedAfterDate != nil {
		createdAfterIssues := filterIssuesByCreationDate(filteredIssues, *filterOptions.CreatedAfterDate, filterOptions)
		printMissingTimeInfoForIssues(createdAfterIssues, *filterOptions.CreatedAfterDate)
	}

	// 新機能2: 前回の指定曜日から1週間の範囲での時間情報を表示
	if filterOptions.WeeklyPeriod != nil {
		weeklyIssues := filterIssuesByWeeklyPeriod(allIssues, *filterOptions.WeeklyPeriod, filterOptions)
		printWeeklyTimeInfo(weeklyIssues, *filterOptions.WeeklyPeriod)

		// 新機能3: 個人別の週間時間情報を表示
		printWeeklyTimeInfoByPerson(weeklyIssues, *filterOptions.WeeklyPeriod)
	}
}

// fetchAllProjectIssues はプロジェクトからすべてのIssueを取得する（GitHubv4クライアント使用）
func fetchAllProjectIssues(client *githubv4.Client, ctx context.Context, org string, projectNum int) ([]IssueTimeInfo, error) {
	var allIssues []IssueTimeInfo
	var query queryProjectV2
	variables := map[string]interface{}{
		"org":        githubv4.String(org),
		"projectNum": githubv4.Int(projectNum),
		"cursor":     (*githubv4.String)(nil), // 初回は nil
	}

	// ページネーション処理
	for {
		err := client.Query(ctx, &query, variables)
		if err != nil {
			return nil, fmt.Errorf("querying GitHub GraphQL API: %w", err)
		}

		// 各Issueを処理
		for _, node := range query.Organization.ProjectV2.Items.Nodes {
			// Issueでない場合はスキップ
			if node.Content.TypeName != "Issue" {
				continue
			}

			issue := node.Content.Issue

			// CreatedAtを取得し、JSTに変換
			createdAtJST := issue.CreatedAt.In(jst)

			// ClosedAtを取得し、JSTに変換 (nilの場合はそのまま)
			var closedAtJST *time.Time
			if issue.ClosedAt != nil {
				closedAt := issue.ClosedAt.In(jst)
				closedAtJST = &closedAt
			}

			// アサイニーの取得
			assignees := make([]string, 0, len(issue.Assignees.Nodes))
			for _, assignee := range issue.Assignees.Nodes {
				assignees = append(assignees, assignee.Login)
			}

			// ラベルの取得
			labels := make([]string, 0, len(issue.Labels.Nodes))
			for _, label := range issue.Labels.Nodes {
				labels = append(labels, label.Name)
			}

			// 状態理由の取得
			stateReason := ""
			if issue.StateReason != nil {
				stateReason = *issue.StateReason
			}

			// カスタムフィールドから見積時間と実績時間を取得
			estimatedTime, actualTime := -1.0, -1.0

			for _, fieldValue := range node.FieldValues.Nodes {
				if fieldValue.TypeName == "ProjectV2ItemFieldNumberValue" {
					if fieldValue.NumberField.Field.Name == estimatedLabel && fieldValue.NumberField.Number != nil {
						estimatedTime = *fieldValue.NumberField.Number
					} else if fieldValue.NumberField.Field.Name == actualLabel && fieldValue.NumberField.Number != nil {
						actualTime = *fieldValue.NumberField.Number
					}
				}
			}

			// IssueTimeInfoの作成
			issueInfo := IssueTimeInfo{
				IssueURL:      issue.URL,
				Title:         issue.Title,
				Author:        issue.Author.Login,
				Assignees:     assignees,
				CreatedAt:     createdAtJST,
				ClosedAt:      closedAtJST,
				State:         issue.State,
				StateReason:   stateReason,
				EstimatedTime: estimatedTime,
				ActualTime:    actualTime,
				Labels:        labels,
			}

			allIssues = append(allIssues, issueInfo)
		}

		// ページネーション処理
		if !query.Organization.ProjectV2.Items.PageInfo.HasNextPage {
			break
		}

		// 次のページのカーソルをセット
		variables["cursor"] = githubv4.String(query.Organization.ProjectV2.Items.PageInfo.EndCursor)
	}

	return allIssues, nil
}

// filterIssues は指定されたフィルターオプションに基づいてIssueをフィルタリングする
func filterIssues(issues []IssueTimeInfo, options FilterOptions) []IssueTimeInfo {
	var filtered []IssueTimeInfo

	for _, issue := range issues {
		// リポジトリフィルター
		if !isRepoInAllowedList(issue.IssueURL, options.AllowedRepositories) {
			continue
		}

		// sbiラベルフィルター
		if options.RequireSbiLabel && !containsLabel(issue.Labels, sbiLabel) {
			continue
		}

		// NOT_PLANNEDでクローズされたIssueを除外
		if options.ExcludeNotPlanned && issue.StateReason == "NOT_PLANNED" {
			continue
		}

		// 閉じられたIssueのみを対象とする場合
		if !options.IncludeOpenIssues && (issue.State != "CLOSED" || issue.ClosedAt == nil) {
			continue
		}

		// 閉じられた日付の範囲フィルタリング
		if options.ClosedDateRange != nil && issue.ClosedAt != nil {
			if issue.ClosedAt.Before(options.ClosedDateRange.StartDate) ||
				issue.ClosedAt.After(options.ClosedDateRange.EndDate) {
				continue
			}
		}

		filtered = append(filtered, issue)
	}

	return filtered
}

// filterIssuesByCreationDate は作成日に基づいてIssueをフィルタリングする
func filterIssuesByCreationDate(issues []IssueTimeInfo, startDate time.Time, baseOptions FilterOptions) []IssueTimeInfo {
	var filtered []IssueTimeInfo

	for _, issue := range issues {
		// リポジトリフィルター
		if !isRepoInAllowedList(issue.IssueURL, baseOptions.AllowedRepositories) {
			continue
		}

		// sbiラベルフィルター
		if baseOptions.RequireSbiLabel && !containsLabel(issue.Labels, sbiLabel) {
			continue
		}

		// NOT_PLANNEDでクローズされたIssueを除外
		if baseOptions.ExcludeNotPlanned && issue.StateReason == "NOT_PLANNED" {
			continue
		}

		// 作成日フィルター（指定日以降）
		if issue.CreatedAt.Before(startDate) {
			continue
		}

		filtered = append(filtered, issue)
	}

	return filtered
}

// filterIssuesByWeeklyPeriod は週間期間に基づいてIssueをフィルタリングする
func filterIssuesByWeeklyPeriod(issues []IssueTimeInfo, period WeeklyPeriod, baseOptions FilterOptions) []IssueTimeInfo {
	var filtered []IssueTimeInfo

	for _, issue := range issues {
		// リポジトリフィルター
		if !isRepoInAllowedList(issue.IssueURL, baseOptions.AllowedRepositories) {
			continue
		}

		// sbiラベルフィルター
		if baseOptions.RequireSbiLabel && !containsLabel(issue.Labels, sbiLabel) {
			continue
		}

		// NOT_PLANNEDでクローズされたIssueを除外
		if baseOptions.ExcludeNotPlanned && issue.StateReason == "NOT_PLANNED" {
			continue
		}

		// 閉じられていないIssueはスキップ
		if issue.ClosedAt == nil {
			continue
		}

		// 週間期間内に閉じられたIssueのみを対象とする
		// 期間は StartDate以上 EndDate未満
		if issue.ClosedAt.Before(period.StartDate) || !issue.ClosedAt.Before(period.EndDate) {
			continue
		}

		filtered = append(filtered, issue)
	}

	return filtered
}

// isRepoInAllowedList はリポジトリが許可リスト内にあるかをURLから判断する
func isRepoInAllowedList(issueURL string, allowedRepos []string) bool {
	for _, repo := range allowedRepos {
		repoURL := fmt.Sprintf("https://github.com/%s/%s", strings.Split(issueURL, "/")[3], repo)
		if strings.HasPrefix(issueURL, repoURL) {
			return true
		}
	}
	return false
}

// containsLabel は指定したラベルが含まれているかチェックする
func containsLabel(labels []string, target string) bool {
	for _, label := range labels {
		if strings.EqualFold(label, target) {
			return true
		}
	}
	return false
}

// getGitHubToken はGitHubトークンを環境変数またはファイルから取得する
func getGitHubToken() string {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fn := os.Getenv("GITHUB_TOKEN_FILE")
		if fn == "" {
			log.Fatal("Neither GITHUB_TOKEN nor GITHUB_TOKEN_FILE environment variables are set")
		}

		tmp, err := os.ReadFile(fn)
		if err != nil {
			log.Fatalf("Error reading token file: %v", err)
		}
		token = strings.TrimSpace(string(tmp))
	}

	if token == "" {
		log.Fatal("GitHub token is empty")
	}

	return token
}

// printSummary は取得したIssueのサマリー情報を出力する
func printSummary(issues []IssueTimeInfo) {
	var totalEstimated, totalActual float64
	var countWithEstimate, countWithActual int

	for _, issue := range issues {
		if issue.EstimatedTime >= 0 {
			totalEstimated += issue.EstimatedTime
			countWithEstimate++
		}
		if issue.ActualTime >= 0 {
			totalActual += issue.ActualTime
			countWithActual++
		}
	}

	fmt.Printf("\n## Summary\n\n")
	fmt.Printf("- Total issues: %d\n", len(issues))
	fmt.Printf("- Issues with estimate: %d (%.1f%%)\n",
		countWithEstimate,
		float64(countWithEstimate)/float64(len(issues))*100)
	fmt.Printf("- Issues with actual time: %d (%.1f%%)\n",
		countWithActual,
		float64(countWithActual)/float64(len(issues))*100)
	fmt.Printf("- Total estimated time: %.1f hours\n", totalEstimated)
	fmt.Printf("- Total actual time: %.1f hours\n", totalActual)

	if countWithEstimate > 0 && countWithActual > 0 {
		fmt.Printf("- Estimate vs Actual ratio: %.2f\n", totalActual/totalEstimated)
	}
}

// printMonthlySummary は月ごとのサマリー情報を出力する
func printMonthlySummary(issues []IssueTimeInfo) {
	// 月ごとに集計
	type MonthlyData struct {
		IssueCount     int
		EstimatedTotal float64
		ActualTotal    float64
	}

	monthlyStats := make(map[string]*MonthlyData)

	for _, issue := range issues {
		if issue.ClosedAt == nil {
			continue
		}

		// 月のキーを作成 (YYYY-MM)
		monthKey := issue.ClosedAt.Format("2006-01")

		if _, exists := monthlyStats[monthKey]; !exists {
			monthlyStats[monthKey] = &MonthlyData{}
		}

		monthlyStats[monthKey].IssueCount++

		if issue.EstimatedTime >= 0 {
			monthlyStats[monthKey].EstimatedTotal += issue.EstimatedTime
		}

		if issue.ActualTime >= 0 {
			monthlyStats[monthKey].ActualTotal += issue.ActualTime
		}
	}

	// キーを時系列順にソート
	var keys []string
	for k := range monthlyStats {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 月別サマリーの出力
	fmt.Printf("\n## Monthly Summary\n\n")
	fmt.Printf("| %-7s | %-8s | %-15s | %-15s | %-10s |\n",
		"Month", "Issues", "Est. Total (h)", "Act. Total (h)", "Ratio")
	fmt.Println("|---------|----------|-----------------|-----------------|------------|")

	for _, month := range keys {
		data := monthlyStats[month]
		ratio := 0.0
		if data.EstimatedTotal > 0 {
			ratio = data.ActualTotal / data.EstimatedTotal
		}

		fmt.Printf("| %-7s | %-8d | %-15.1f | %-15.1f | %-10.2f |\n",
			month, data.IssueCount, data.EstimatedTotal, data.ActualTotal, ratio)
	}
}

// printMissingTimeInfoForIssues は指定された日時以降に作成されたIssueで時間情報が欠けているものを出力
func printMissingTimeInfoForIssues(issues []IssueTimeInfo, startDate time.Time) {
	fmt.Printf("\n## Issues Created On or After %s with Missing Time Information\n",
		startDate.Format("2006-01-02"))

	if len(issues) == 0 {
		fmt.Printf("\nNo issues found created on or after %s\n", startDate.Format("2006-01-02"))
		return
	}

	var missingEstimate, missingActual, missingBoth []IssueTimeInfo

	for _, issue := range issues {
		if issue.EstimatedTime < 0 && issue.ActualTime < 0 {
			missingBoth = append(missingBoth, issue)
		} else if issue.EstimatedTime < 0 {
			missingEstimate = append(missingEstimate, issue)
		} else if issue.ActualTime < 0 {
			missingActual = append(missingActual, issue)
		}
	}

	fmt.Printf("\nTotal issues created on or after %s: %d\n",
		startDate.Format("2006-01-02"), len(issues))

	// 両方欠けているIssue
	if len(missingBoth) > 0 {
		fmt.Printf("\n### Issues missing BOTH estimated and actual time (%d):\n\n", len(missingBoth))
		for _, issue := range missingBoth {
			fmt.Printf("- [%s](%s) - Created: %s\n",
				issue.Title, issue.IssueURL, issue.CreatedAt.Format("2006-01-02"))
		}
	}

	// 見積時間が欠けているIssue
	if len(missingEstimate) > 0 {
		fmt.Printf("\n### Issues missing estimated time only (%d):\n\n", len(missingEstimate))
		for _, issue := range missingEstimate {
			fmt.Printf("- [%s](%s) - Created: %s\n",
				issue.Title, issue.IssueURL, issue.CreatedAt.Format("2006-01-02"))
		}
	}

	// 実績時間が欠けているIssue
	if len(missingActual) > 0 {
		fmt.Printf("\n### Issues missing actual time only (%d):\n\n", len(missingActual))
		for _, issue := range missingActual {
			fmt.Printf("- [%s](%s) - Created: %s\n",
				issue.Title, issue.IssueURL, issue.CreatedAt.Format("2006-01-02"))
		}
	}

	// 合計数
	totalMissing := len(missingEstimate) + len(missingActual) + len(missingBoth)
	if len(issues) > 0 {
		fmt.Printf("\nTotal issues created on or after %s with missing time information: %d (%.1f%%)\n",
			startDate.Format("2006-01-02"), totalMissing, float64(totalMissing)/float64(len(issues))*100)
	}
}

// printWeeklyTimeInfo は週間期間での時間情報を表示
func printWeeklyTimeInfo(issues []IssueTimeInfo, period WeeklyPeriod) {
	// 曜日名のマップ
	weekdayNames := map[int]string{
		0: "Sunday",
		1: "Monday",
		2: "Tuesday",
		3: "Wednesday",
		4: "Thursday",
		5: "Friday",
		6: "Saturday",
	}

	// 終了日の前日を表示用に計算（期間は終了日を含まないため）
	displayEndDate := period.EndDate.AddDate(0, 0, -1)

	fmt.Printf("\n## Weekly Time Summary (%s to %s)\n\n",
		period.StartDate.Format("2006-01-02"), displayEndDate.Format("2006-01-02"))
	fmt.Printf("Period: From the %s (%s) before yesterday to %s (%s)\n\n",
		weekdayNames[period.Weekday], period.StartDate.Format("2006-01-02"),
		weekdayNames[(period.Weekday+6)%7], displayEndDate.Format("2006-01-02"))

	if len(issues) == 0 {
		fmt.Printf("No issues closed during this period\n")
		return
	}

	// 時間情報の集計
	var totalEstimated, totalActual float64
	var countWithEstimate, countWithActual int

	for _, issue := range issues {
		if issue.EstimatedTime >= 0 {
			totalEstimated += issue.EstimatedTime
			countWithEstimate++
		}
		if issue.ActualTime >= 0 {
			totalActual += issue.ActualTime
			countWithActual++
		}
	}

	// 集計結果の出力
	fmt.Printf("- Total issues closed in this period: %d\n", len(issues))
	fmt.Printf("- Issues with estimate: %d\n", countWithEstimate)
	fmt.Printf("- Issues with actual time: %d\n", countWithActual)
	fmt.Printf("- Total estimated time: %.1f hours\n", totalEstimated)
	fmt.Printf("- Total actual time: %.1f hours\n", totalActual)

	// 平均値の計算と出力
	if countWithEstimate > 0 {
		fmt.Printf("- Average estimated time per issue: %.1f hours\n", totalEstimated/float64(countWithEstimate))
	} else {
		fmt.Printf("- Average estimated time per issue: N/A (no issues with estimates)\n")
	}

	if countWithActual > 0 {
		fmt.Printf("- Average actual time per issue: %.1f hours\n", totalActual/float64(countWithActual))
	} else {
		fmt.Printf("- Average actual time per issue: N/A (no issues with actual time)\n")
	}

	if countWithEstimate > 0 && countWithActual > 0 {
		fmt.Printf("- Estimate vs Actual ratio: %.2f\n", totalActual/totalEstimated)
	} else {
		fmt.Printf("- Estimate vs Actual ratio: N/A (missing data)\n")
	}

	// 範囲内のIssueリストを出力
	fmt.Printf("\n### Issues closed during this period:\n\n")
	for i, issue := range issues {
		estTime := "N/A"
		if issue.EstimatedTime >= 0 {
			estTime = fmt.Sprintf("%.1f", issue.EstimatedTime)
		}

		actTime := "N/A"
		if issue.ActualTime >= 0 {
			actTime = fmt.Sprintf("%.1f", issue.ActualTime)
		}

		fmt.Printf("%d. [%s](%s) - Closed: %s - Est/Act: %s/%s hours\n",
			i+1, issue.Title, issue.IssueURL, issue.ClosedAt.Format("2006-01-02"), estTime, actTime)
	}
}
