package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	estimatedLabel = "見積時間"
	actualLabel    = "実績時間"
	sizeLabel      = "Size" // サイズラベルの定数を追加
	sbiLabel       = "sbi"
	pbiLabel       = "pbi"       // pbiラベルの定数を追加
	devPbiLabel    = "dev-pbi"   // dev-pbiラベルの定数を追加
	jstOffset      = 9 * 60 * 60 // JSTは UTC+9時間
)

// JSTの定義（パッケージレベルで定義）
var jst = time.FixedZone("JST", jstOffset)

// IssueTimeInfo はIssueの時間情報を格納する構造体
type IssueTimeInfo struct {
	IssueURL      string          `json:"issue_url"`
	Title         string          `json:"title"`
	Author        string          `json:"author"`
	Assignees     []string        `json:"assignees"`
	CreatedAt     time.Time       `json:"created_at"`
	ClosedAt      *time.Time      `json:"closed_at"`
	State         string          `json:"state"`
	StateReason   string          `json:"state_reason"`
	EstimatedTime float64         `json:"estimated_time"`
	ActualTime    float64         `json:"actual_time"`
	Size          float64         `json:"size"`
	Labels        []string        `json:"labels"`
	HasParent     bool            `json:"has_parent"`
	SubIssues     []IssueTimeInfo `json:"sub_issues"` // 子Issueのリスト
}

// GraphQLClient はGraphQL APIへのリクエストを処理する簡易クライアント
type GraphQLClient struct {
	httpClient *http.Client
	endpoint   string
	token      string
}

// GraphQLRequest はGraphQLリクエストを表す構造体
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

// GraphQLResponse はGraphQLレスポンスを表す構造体
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// ProjectQueryResponse はプロジェクトクエリのレスポンス構造
type ProjectQueryResponse struct {
	Organization struct {
		ProjectV2 struct {
			Title string
			Items struct {
				PageInfo struct {
					HasNextPage bool
					EndCursor   *string
				}
				Nodes []struct {
					Content struct {
						TypeName    string `json:"__typename"`
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
						}
						Assignees struct {
							Nodes []struct {
								Login string
							}
						}
						URL        string
						Repository struct {
							Name string
						}
						CreatedAt string // Issueの作成日時
						ClosedAt  *string
						Parent    *struct { // 親Issueの情報
							ID string
						}
					} `json:"content"`
					FieldValues struct {
						Nodes []struct {
							TypeName string `json:"__typename"`
							// 数値フィールド用（見積時間、実績時間など）
							Field struct {
								Name string
							} `json:"field,omitempty"`
							Number *float64 `json:"number,omitempty"`
							// 以下は他のフィールドタイプ用だが、今回は使用しない
							Name  *string `json:"name,omitempty"`
							Title string  `json:"title,omitempty"`
							Text  string  `json:"text,omitempty"`
							Date  string  `json:"date,omitempty"`
						}
					}
				}
			}
		}
	}
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

// RuleViolation はルール違反の情報を格納する構造体
type RuleViolation struct {
	IssueURL  string   // IssueのURL
	Title     string   // Issueのタイトル
	Assignees []string // アサインされた人々
	Author    string   // 作成者
	Reason    string   // 違反理由
}

// checkRuleViolations はIssueがルールに準拠しているかをチェックする
func checkRuleViolations(issues []IssueTimeInfo) []RuleViolation {
	var violations []RuleViolation

	// 再帰的にIssueとその子Issueをチェックする内部関数
	var checkRecursively func(issue IssueTimeInfo)
	checkRecursively = func(issue IssueTimeInfo) {
		// デバッグ情報
		log.Printf("Checking issue #%s: %s", getIssueNumberFromURL(issue.IssueURL), issue.Title)
		log.Printf("  Labels: %v", issue.Labels)
		log.Printf("  Size: %.1f, EstimatedTime: %.1f, ActualTime: %.1f", issue.Size, issue.EstimatedTime, issue.ActualTime)

		// ラベルチェック - 大文字小文字を区別しない
		hasPBI := containsLabelCaseInsensitive(issue.Labels, "pbi") || containsLabelCaseInsensitive(issue.Labels, "dev-pbi")
		hasSBI := containsLabelCaseInsensitive(issue.Labels, "sbi") || containsLabelCaseInsensitive(issue.Labels, "dev-sbi")

		// 違反チェック
		var reason string

		if hasPBI && issue.Size < 0 {
			reason = "pbi/dev-pbiラベルが付いているがSizeが設定されていません"
		}

		if hasSBI {
			missingFields := []string{}

			if issue.EstimatedTime < 0 {
				missingFields = append(missingFields, "見積時間")
			}

			if issue.ActualTime < 0 {
				missingFields = append(missingFields, "実績時間")
			}

			if len(missingFields) > 0 {
				reason = "sbi/dev-sbiラベルが付いていますが、" + strings.Join(missingFields, "と") + "が設定されていません"
			}

			// 難易度ラベルのチェック
			hasDifficultyLabel := false
			difficultyLabels := []string{"difficulty:low", "difficulty:medium", "difficulty:high"}

			for _, label := range difficultyLabels {
				if containsLabelCaseInsensitive(issue.Labels, label) {
					hasDifficultyLabel = true
					break
				}
			}

			if !hasDifficultyLabel {
				if reason != "" {
					reason += "。また、"
				}
				reason += "難易度ラベル(difficulty:low/medium/high)が設定されていません"
			}
		}

		// 違反があれば記録
		if reason != "" {
			responsible := issue.Assignees
			if len(responsible) == 0 {
				responsible = []string{issue.Author}
			}

			violations = append(violations, RuleViolation{
				IssueURL:  issue.IssueURL,
				Title:     issue.Title,
				Assignees: responsible,
				Author:    issue.Author,
				Reason:    reason,
			})
		}

		// 子Issueを再帰的にチェック
		for _, subIssue := range issue.SubIssues {
			checkRecursively(subIssue)
		}
	}

	// 全てのトップレベルIssueをチェック
	for _, issue := range issues {
		checkRecursively(issue)
	}

	return violations
}

// printRuleViolations はルール違反の情報を表示する
func printRuleViolations(violations []RuleViolation) {
	if len(violations) == 0 {
		fmt.Println("\n## ルール違反チェック\n\nルール違反は見つかりませんでした。全てのIssueは正しく設定されています。")
		return
	}

	fmt.Printf("\n## ルール違反チェック\n\n合計 %d 件のルール違反が見つかりました。\n\n", len(violations))

	for i, violation := range violations {
		issueNum := getIssueNumberFromURL(violation.IssueURL)
		fmt.Printf("%d. **Issue #%s**: [%s](%s)\n", i+1, issueNum, violation.Title, violation.IssueURL)

		// 担当者を表示
		responsible := strings.Join(violation.Assignees, ", ")
		fmt.Printf("   - 担当者: %s\n", responsible)

		// 違反理由
		fmt.Printf("   - 違反内容: %s\n\n", violation.Reason)
	}
}

// SubIssueQueryResponse は特定のIssueの子Issueを取得するためのレスポンス構造
type SubIssueQueryResponse struct {
	Repository struct {
		Issue struct {
			Title     string
			SubIssues struct {
				PageInfo struct {
					HasNextPage bool
					EndCursor   *string
				}
				Edges []struct {
					Node struct {
						Id          string
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
						}
						Assignees struct {
							Nodes []struct {
								Login string
							}
						}
						URL        string
						CreatedAt  string
						ClosedAt   *string
						Repository struct {
							Name  string
							Owner struct {
								Login string
							}
						}
						ProjectItems struct {
							Nodes []struct {
								Project struct {
									Title  string
									Number int
								}
								FieldValues struct {
									Nodes []struct {
										TypeName string `json:"__typename"`
										Field    struct {
											Name string
										} `json:"field,omitempty"`
										Number *float64 `json:"number,omitempty"`
									}
								}
							}
						}
					}
				}
			} `json:"subIssues"`
		} `json:"issue"`
	} `json:"repository"`
}

// TopLevelIssueWithSubIssues はトップレベルIssueとそのサブIssueを格納する構造体
type TopLevelIssueWithSubIssues struct {
	TopLevelIssue IssueTimeInfo
	SubIssues     []IssueTimeInfo
}

// IssueSummary はIssueのサマリー情報を格納する構造体
type IssueSummary struct {
	IssueURL         string   // IssueのURL
	Title            string   // Issueタイトル
	Size             float64  // トップレベルIssueのSize
	TotalEstimated   float64  // 子孫Issueの見積時間合計
	TotalActual      float64  // 子孫Issueの実績時間合計
	SubIssueCount    int      // 子孫Issueの数
	HasRuleViolation bool     // ルール違反があるか
	Violations       []string // 違反内容のリスト
}

// NewGraphQLClient は新しいGraphQLクライアントを作成する
func NewGraphQLClient(token string) *GraphQLClient {
	return &GraphQLClient{
		httpClient: &http.Client{},
		endpoint:   "https://api.github.com/graphql",
		token:      token,
	}
}

// Execute はGraphQLクエリを実行する
func (c *GraphQLClient) Execute(ctx context.Context, query string, variables map[string]interface{}, responseData interface{}) error {
	// リクエストの準備
	req := GraphQLRequest{
		Query:     query,
		Variables: variables,
	}
	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	// HTTPリクエストの作成
	httpReq, err := http.NewRequest("POST", c.endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Authorization", "bearer "+c.token)
	httpReq.Header.Set("Content-Type", "application/json")
	// Sub-Issue機能を有効にするためのヘッダーを追加
	httpReq.Header.Set("GraphQL-Features", "sub_issues")

	// リクエストの実行
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// レスポンスの解析
	var graphqlResp GraphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&graphqlResp); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	// エラーチェック
	if len(graphqlResp.Errors) > 0 {
		return fmt.Errorf("graphql errors: %s", graphqlResp.Errors[0].Message)
	}

	// データの解析
	if err := json.Unmarshal(graphqlResp.Data, responseData); err != nil {
		return fmt.Errorf("unmarshaling data: %w", err)
	}

	return nil
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

	// フィルターオプションの作成 - ラベル要件を削除
	filterOptions := FilterOptions{
		IncludeOpenIssues:   false, // 閉じられたIssueのみ対象
		RequireSbiLabel:     false, // ラベル判定は使用しない
		ExcludeNotPlanned:   false, // COMPLETEDで終了したIssueだけを含める
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
	client := NewGraphQLClient(token)
	ctx := context.Background()

	// プロジェクトからIssueを取得
	allIssues, err := fetchAllProjectIssues(client, ctx, org, projectNum)
	if err != nil {
		log.Fatalf("Error fetching issues from project: %v", err)
	}

	// フィルタリングを適用
	filteredTopLevelIssues := filterIssues(allIssues, filterOptions)

	// 結果の出力
	if len(filteredTopLevelIssues) == 0 {
		fmt.Println("No issues found matching the criteria")
		return
	}

	fmt.Printf("Found %d issues matching criteria in repositories: %s\n\n",
		len(filteredTopLevelIssues), strings.Join(repos, ", "))

	// サマリー情報を出力
	printSummary(filteredTopLevelIssues)

	// 月ごとのサマリー
	printMonthlySummary(filteredTopLevelIssues)

	// 新機能1: 指定された日時以降に作成されたIssueで時間情報が欠けているものを出力
	if filterOptions.CreatedAfterDate != nil {
		createdAfterIssues := filterIssuesByCreationDate(filteredTopLevelIssues, *filterOptions.CreatedAfterDate, filterOptions)
		printMissingTimeInfoForIssues(createdAfterIssues, *filterOptions.CreatedAfterDate)
	}

	// 新機能2: 前回の指定曜日から1週間の範囲での時間情報を表示
	if filterOptions.WeeklyPeriod != nil {
		weeklyIssues := filterIssuesByWeeklyPeriod(allIssues, *filterOptions.WeeklyPeriod, filterOptions)
		printWeeklyTimeInfo(weeklyIssues, *filterOptions.WeeklyPeriod)

		// 新機能3: 個人別の週間時間情報を表示
		printWeeklyTimeInfoByPerson(weeklyIssues, *filterOptions.WeeklyPeriod)
	}

	// フィルタリングされたIssueの表示
	printFilteredIssues(filteredTopLevelIssues)

	// 新機能: トップレベルIssueに再帰的にサブIssueを追加
	log.Println("Fetching sub-issues hierarchically for top-level issues...")

	// 再帰の最大深さを設定 (例：5レベルまで)
	maxRecursionDepth := 5

	enrichedIssues, err := enrichIssuesWithSubIssues(client, ctx, filteredTopLevelIssues, maxRecursionDepth)
	if err != nil {
		log.Printf("Warning: Error enriching issues with sub-issues: %v", err)
	} else {
		// 階層構造の表示
		printIssuesWithHierarchy(enrichedIssues)

		// 階層の統計情報を表示
		printIssueHierarchyStats(enrichedIssues)

		// ルール違反のチェック
		log.Println("Checking rule violations...")
		violations := checkRuleViolations(enrichedIssues)
		printRuleViolations(violations)

		// main関数の最後に追加（ルール違反チェックの後）

		// トップレベルIssueごとのサマリー情報を計算
		log.Println("Calculating issue summaries...")
		summaries := calculateIssueSummaries(enrichedIssues)

		// サマリー情報を表示
		printIssueSummaries(summaries)
	}
}

// fetchAllProjectIssues はプロジェクトからすべてのIssueを取得する（フィルタリングなし）
func fetchAllProjectIssues(client *GraphQLClient, ctx context.Context, org string, projectNum int) ([]IssueTimeInfo, error) {
	var allIssues []IssueTimeInfo
	cursor := ""

	// GraphQLクエリの準備 - parentフィールドを追加
	query := `
	query ProjectIssues($org: String!, $projectNum: Int!, $cursor: String) {
		organization(login: $org) {
			projectV2(number: $projectNum) {
				title
				items(first: 100, after: $cursor) {
					pageInfo {
						hasNextPage
						endCursor
					}
					nodes {
						content {
							__typename
							... on Issue {
								number
								title
								state
								stateReason
								author {
									login
								}
								labels(first: 100) {
									nodes {
										name
									}
								}
								assignees(first: 10) {
									nodes {
										login
									}
								}
								url
								repository {
									name
								}
								createdAt
								closedAt
								parent {
									id
								}
							}
						}
						fieldValues(first: 100) {
							nodes {
								__typename
								... on ProjectV2ItemFieldNumberValue {
									field {
										... on ProjectV2FieldCommon {
											name
										}
									}
									number
								}
							}
						}
					}
				}
			}
		}
	}`

	// ページネーション処理
	for {
		variables := map[string]interface{}{
			"org":        org,
			"projectNum": projectNum,
		}

		if cursor != "" {
			variables["cursor"] = cursor
		}

		var response ProjectQueryResponse
		err := client.Execute(ctx, query, variables, &response)
		if err != nil {
			return nil, fmt.Errorf("executing GraphQL query: %w", err)
		}

		// 各Issueを処理
		for _, node := range response.Organization.ProjectV2.Items.Nodes {
			// Issueでない場合はスキップ
			if node.Content.TypeName != "Issue" {
				continue
			}

			// 作成日時をパース
			createdAtUTC, err := time.Parse(time.RFC3339, node.Content.CreatedAt)
			if err != nil {
				log.Printf("Error parsing createdAt time for issue #%d: %v", node.Content.Number, err)
				continue
			}
			// UTCからJSTへ変換
			createdAtJST := createdAtUTC.In(jst)

			// 閉じられた日時をパース
			var closedAt *time.Time
			if node.Content.ClosedAt != nil {
				// GitHubから返される時刻はUTCなのでパース後にJSTに変換
				parsedTimeUTC, err := time.Parse(time.RFC3339, *node.Content.ClosedAt)
				if err != nil {
					log.Printf("Error parsing closedAt time for issue #%d: %v", node.Content.Number, err)
					continue
				}

				// UTCからJSTに変換
				parsedTimeJST := parsedTimeUTC.In(jst)
				closedAt = &parsedTimeJST
			}

			// アサインされたユーザーの取得
			assignees := make([]string, 0, len(node.Content.Assignees.Nodes))
			for _, assignee := range node.Content.Assignees.Nodes {
				assignees = append(assignees, assignee.Login)
			}

			// ラベルの取得
			labels := make([]string, 0, len(node.Content.Labels.Nodes))
			for _, label := range node.Content.Labels.Nodes {
				labels = append(labels, label.Name)
			}

			// 状態理由の取得
			stateReason := ""
			if node.Content.StateReason != nil {
				stateReason = *node.Content.StateReason
			}

			// 親Issueを持つかどうかを判定
			hasParent := node.Content.Parent != nil

			// カスタムフィールドから見積時間と実績時間とサイズを取得
			estimatedTime, actualTime, size := -1.0, -1.0, -1.0

			for _, fieldValue := range node.FieldValues.Nodes {
				if fieldValue.TypeName == "ProjectV2ItemFieldNumberValue" {
					if fieldValue.Field.Name == estimatedLabel && fieldValue.Number != nil {
						estimatedTime = *fieldValue.Number
					} else if fieldValue.Field.Name == actualLabel && fieldValue.Number != nil {
						actualTime = *fieldValue.Number
					} else if fieldValue.Field.Name == "Size" && fieldValue.Number != nil {
						size = *fieldValue.Number
					}
				}
			}

			// IssueTimeInfoの作成
			issueInfo := IssueTimeInfo{
				IssueURL:      node.Content.URL,
				Title:         node.Content.Title,
				Author:        node.Content.Author.Login,
				Assignees:     assignees,
				CreatedAt:     createdAtJST,
				ClosedAt:      closedAt,
				State:         node.Content.State,
				StateReason:   stateReason,
				EstimatedTime: estimatedTime,
				ActualTime:    actualTime,
				Size:          size,
				Labels:        labels,
				HasParent:     hasParent,
			}

			allIssues = append(allIssues, issueInfo)
		}

		// ページネーション処理
		if !response.Organization.ProjectV2.Items.PageInfo.HasNextPage {
			break
		}

		cursor = *response.Organization.ProjectV2.Items.PageInfo.EndCursor
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

		// 親Issueを持つIssueは除外 (トップレベルIssueのみを対象とする)
		if issue.HasParent {
			continue
		}

		// 状態フィルター: "CLOSED"かつ"COMPLETED"のものを対象とする
		if issue.State != "CLOSED" || issue.StateReason != "COMPLETED" {
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

		// 親Issueを持つIssueは除外 (トップレベルIssueのみを対象とする)
		if issue.HasParent {
			continue
		}

		// 状態フィルター: "CLOSED"かつ"COMPLETED"のものを対象とする
		if issue.State != "CLOSED" || issue.StateReason != "COMPLETED" {
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

		// 親Issueを持つIssueは除外 (トップレベルIssueのみを対象とする)
		if issue.HasParent {
			continue
		}

		// 状態フィルター: "CLOSED"かつ"COMPLETED"のものを対象とする
		if issue.State != "CLOSED" || issue.StateReason != "COMPLETED" {
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
	var totalEstimated, totalActual, totalSize float64
	var countWithEstimate, countWithActual, countWithSize int

	for _, issue := range issues {
		if issue.EstimatedTime >= 0 {
			totalEstimated += issue.EstimatedTime
			countWithEstimate++
		}
		if issue.ActualTime >= 0 {
			totalActual += issue.ActualTime
			countWithActual++
		}
		if issue.Size >= 0 {
			totalSize += issue.Size
			countWithSize++
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
	fmt.Printf("- Issues with size: %d (%.1f%%)\n",
		countWithSize,
		float64(countWithSize)/float64(len(issues))*100)
	fmt.Printf("- Total estimated time: %.1f hours\n", totalEstimated)
	fmt.Printf("- Total actual time: %.1f hours\n", totalActual)
	fmt.Printf("- Total size: %.1f\n", totalSize)

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

// printMissingTimeInfo は見積時間または実績時間が設定されていないIssueの情報を出力する
func printMissingTimeInfo(issues []IssueTimeInfo) {
	fmt.Printf("\n## Issues with Missing Time Information\n")

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

	// 両方欠けているIssue
	if len(missingBoth) > 0 {
		fmt.Printf("\n### Issues missing BOTH estimated and actual time (%d):\n\n", len(missingBoth))
		for _, issue := range missingBoth {
			fmt.Printf("- [%s](%s)\n", issue.Title, issue.IssueURL)
		}
	}

	// 見積時間が欠けているIssue
	if len(missingEstimate) > 0 {
		fmt.Printf("\n### Issues missing estimated time only (%d):\n\n", len(missingEstimate))
		for _, issue := range missingEstimate {
			fmt.Printf("- [%s](%s)\n", issue.Title, issue.IssueURL)
		}
	}

	// 実績時間が欠けているIssue
	if len(missingActual) > 0 {
		fmt.Printf("\n### Issues missing actual time only (%d):\n\n", len(missingActual))
		for _, issue := range missingActual {
			fmt.Printf("- [%s](%s)\n", issue.Title, issue.IssueURL)
		}
	}

	// 合計数
	totalMissing := len(missingEstimate) + len(missingActual) + len(missingBoth)
	fmt.Printf("\nTotal issues with missing time information: %d (%.1f%%)\n",
		totalMissing, float64(totalMissing)/float64(len(issues))*100)
}

// printWeeklyTimeInfoByPerson は週間期間での個人別時間情報を表示
func printWeeklyTimeInfoByPerson(issues []IssueTimeInfo, period WeeklyPeriod) {
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

	fmt.Printf("\n## Weekly Time Summary By Person (%s to %s)\n\n",
		period.StartDate.Format("2006-01-02"), displayEndDate.Format("2006-01-02"))
	fmt.Printf("Period: From the %s (%s) before yesterday to %s (%s)\n\n",
		weekdayNames[period.Weekday], period.StartDate.Format("2006-01-02"),
		weekdayNames[(period.Weekday+6)%7], displayEndDate.Format("2006-01-02"))

	if len(issues) == 0 {
		fmt.Printf("No issues closed during this period\n")
		return
	}

	// 個人ごとのデータを格納する構造体
	type PersonData struct {
		Issues            []IssueTimeInfo
		TotalEstimated    float64
		TotalActual       float64
		CountWithEstimate int
		CountWithActual   int
		MissingTimeInfo   []IssueTimeInfo // 時間情報が欠けているIssue
	}

	// 個人ごとのデータを集計
	personStats := make(map[string]*PersonData)
	var unassignedIssues []IssueTimeInfo

	for _, issue := range issues {
		// アサイニーがいない場合は未割り当てとして扱う
		if len(issue.Assignees) == 0 {
			unassignedIssues = append(unassignedIssues, issue)
			continue
		}

		// 各アサイニーに対して処理
		for _, assignee := range issue.Assignees {
			if _, exists := personStats[assignee]; !exists {
				personStats[assignee] = &PersonData{}
			}

			// Issueを追加
			personStats[assignee].Issues = append(personStats[assignee].Issues, issue)

			// 時間情報を集計
			if issue.EstimatedTime >= 0 {
				personStats[assignee].TotalEstimated += issue.EstimatedTime
				personStats[assignee].CountWithEstimate++
			}

			if issue.ActualTime >= 0 {
				personStats[assignee].TotalActual += issue.ActualTime
				personStats[assignee].CountWithActual++
			}

			// 時間情報が欠けているIssueを記録
			if issue.EstimatedTime < 0 || issue.ActualTime < 0 {
				personStats[assignee].MissingTimeInfo = append(personStats[assignee].MissingTimeInfo, issue)
			}
		}
	}

	// 個人別のサマリーを出力
	fmt.Printf("### Summary By Person\n\n")
	fmt.Printf("| %-15s | %-8s | %-15s | %-15s | %-10s | %-17s |\n",
		"Person", "Issues", "Est. Total (h)", "Act. Total (h)", "Ratio", "Issues Missing Time")
	fmt.Println("|-----------------|----------|-----------------|-----------------|------------|-------------------|")

	// アサイニー名でソートするためのキーリスト
	var keys []string
	for k := range personStats {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 個人ごとの情報を出力
	for _, person := range keys {
		data := personStats[person]
		ratio := 0.0
		if data.TotalEstimated > 0 {
			ratio = data.TotalActual / data.TotalEstimated
		}

		fmt.Printf("| %-15s | %-8d | %-15.1f | %-15.1f | %-10.2f | %-17d |\n",
			person, len(data.Issues), data.TotalEstimated, data.TotalActual, ratio, len(data.MissingTimeInfo))
	}

	// 未割り当てIssueがあれば出力
	if len(unassignedIssues) > 0 {
		var totalEstUnassigned, totalActUnassigned float64
		var countEstUnassigned, countActUnassigned int
		var missingTimeUnassigned []IssueTimeInfo

		for _, issue := range unassignedIssues {
			if issue.EstimatedTime >= 0 {
				totalEstUnassigned += issue.EstimatedTime
				countEstUnassigned++
			}
			if issue.ActualTime >= 0 {
				totalActUnassigned += issue.ActualTime
				countActUnassigned++
			}
			if issue.EstimatedTime < 0 || issue.ActualTime < 0 {
				missingTimeUnassigned = append(missingTimeUnassigned, issue)
			}
		}

		ratio := 0.0
		if totalEstUnassigned > 0 {
			ratio = totalActUnassigned / totalEstUnassigned
		}

		fmt.Printf("| %-15s | %-8d | %-15.1f | %-15.1f | %-10.2f | %-17d |\n",
			"Unassigned", len(unassignedIssues), totalEstUnassigned, totalActUnassigned, ratio, len(missingTimeUnassigned))
	}

	// 個人ごとの詳細情報を出力
	fmt.Printf("\n### Details By Person\n\n")

	for _, person := range keys {
		data := personStats[person]
		fmt.Printf("#### %s\n\n", person)

		// 基本統計
		fmt.Printf("- Total issues closed: %d\n", len(data.Issues))
		fmt.Printf("- Issues with estimate: %d\n", data.CountWithEstimate)
		fmt.Printf("- Issues with actual time: %d\n", data.CountWithActual)
		fmt.Printf("- Total estimated time: %.1f hours\n", data.TotalEstimated)
		fmt.Printf("- Total actual time: %.1f hours\n", data.TotalActual)

		// 平均値の計算と出力
		if data.CountWithEstimate > 0 {
			fmt.Printf("- Average estimated time per issue: %.1f hours\n",
				data.TotalEstimated/float64(data.CountWithEstimate))
		} else {
			fmt.Printf("- Average estimated time per issue: N/A (no issues with estimates)\n")
		}

		if data.CountWithActual > 0 {
			fmt.Printf("- Average actual time per issue: %.1f hours\n",
				data.TotalActual/float64(data.CountWithActual))
		} else {
			fmt.Printf("- Average actual time per issue: N/A (no issues with actual time)\n")
		}

		if data.CountWithEstimate > 0 && data.CountWithActual > 0 {
			fmt.Printf("- Estimate vs Actual ratio: %.2f\n", data.TotalActual/data.TotalEstimated)
		} else {
			fmt.Printf("- Estimate vs Actual ratio: N/A (missing data)\n")
		}

		// 担当Issueリスト
		fmt.Printf("\n##### Issues:\n\n")
		for i, issue := range data.Issues {
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

		// 時間情報が欠けているIssueリスト
		if len(data.MissingTimeInfo) > 0 {
			fmt.Printf("\n##### Issues with Missing Time Information:\n\n")
			for i, issue := range data.MissingTimeInfo {
				estTime := "N/A"
				if issue.EstimatedTime >= 0 {
					estTime = fmt.Sprintf("%.1f", issue.EstimatedTime)
				}

				actTime := "N/A"
				if issue.ActualTime >= 0 {
					actTime = fmt.Sprintf("%.1f", issue.ActualTime)
				}

				fmt.Printf("%d. [%s](%s) - Missing: Est=%s, Act=%s\n",
					i+1, issue.Title, issue.IssueURL, estTime, actTime)
			}
		}

		fmt.Println()
	}

	// 未割り当てIssueがあれば詳細を出力
	if len(unassignedIssues) > 0 {
		fmt.Printf("#### Unassigned Issues\n\n")

		// 統計情報
		var totalEstUnassigned, totalActUnassigned float64
		var countEstUnassigned, countActUnassigned int
		var missingTimeUnassigned []IssueTimeInfo

		for _, issue := range unassignedIssues {
			if issue.EstimatedTime >= 0 {
				totalEstUnassigned += issue.EstimatedTime
				countEstUnassigned++
			}
			if issue.ActualTime >= 0 {
				totalActUnassigned += issue.ActualTime
				countActUnassigned++
			}
			if issue.EstimatedTime < 0 || issue.ActualTime < 0 {
				missingTimeUnassigned = append(missingTimeUnassigned, issue)
			}
		}

		// 基本統計
		fmt.Printf("- Total unassigned issues closed: %d\n", len(unassignedIssues))
		fmt.Printf("- Issues with estimate: %d\n", countEstUnassigned)
		fmt.Printf("- Issues with actual time: %d\n", countActUnassigned)
		fmt.Printf("- Total estimated time: %.1f hours\n", totalEstUnassigned)
		fmt.Printf("- Total actual time: %.1f hours\n", totalActUnassigned)

		// 未割り当てIssueリスト
		fmt.Printf("\n##### Unassigned Issues:\n\n")
		for i, issue := range unassignedIssues {
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

		// 時間情報が欠けているIssueリスト
		if len(missingTimeUnassigned) > 0 {
			fmt.Printf("\n##### Unassigned Issues with Missing Time Information:\n\n")
			for i, issue := range missingTimeUnassigned {
				estTime := "N/A"
				if issue.EstimatedTime >= 0 {
					estTime = fmt.Sprintf("%.1f", issue.EstimatedTime)
				}

				actTime := "N/A"
				if issue.ActualTime >= 0 {
					actTime = fmt.Sprintf("%.1f", issue.ActualTime)
				}

				fmt.Printf("%d. [%s](%s) - Missing: Est=%s, Act=%s\n",
					i+1, issue.Title, issue.IssueURL, estTime, actTime)
			}
		}
	}
}

// printFilteredIssues は条件に一致するIssueを表示する
func printFilteredIssues(issues []IssueTimeInfo) {
	fmt.Printf("\n## Issues meeting criteria (COMPLETED state, top level issues)\n\n")

	if len(issues) == 0 {
		fmt.Println("No issues found meeting the criteria.")
		return
	}

	fmt.Printf("| %-6s | %-40s | %-10s | %-10s | %-10s | %-15s |\n",
		"Issue", "Title", "Est (h)", "Act (h)", "Size", "Labels")
	fmt.Println("|--------|------------------------------------------|------------|------------|------------|-----------------|")

	for _, issue := range issues {
		// ラベルを文字列に変換
		labelsStr := strings.Join(issue.Labels, ", ")
		if len(labelsStr) > 15 {
			labelsStr = labelsStr[:12] + "..."
		}

		// 数値フィールドの表示形式
		estTime := "N/A"
		if issue.EstimatedTime >= 0 {
			estTime = fmt.Sprintf("%.1f", issue.EstimatedTime)
		}

		actTime := "N/A"
		if issue.ActualTime >= 0 {
			actTime = fmt.Sprintf("%.1f", issue.ActualTime)
		}

		size := "N/A"
		if issue.Size >= 0 {
			size = fmt.Sprintf("%.1f", issue.Size)
		}

		// Issue番号を抽出
		issueNum := "?"
		parts := strings.Split(issue.IssueURL, "/")
		if len(parts) > 0 {
			issueNum = parts[len(parts)-1]
		}

		// タイトルが長すぎる場合は切り詰める
		title := issue.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}

		fmt.Printf("| %-6s | %-40s | %-10s | %-10s | %-10s | %-15s |\n",
			issueNum, title, estTime, actTime, size, labelsStr)
	}
}

// fetchSubIssuesForIssue は特定のトップレベルIssueに紐づくサブIssueを取得する
func fetchSubIssuesForIssue(client *GraphQLClient, ctx context.Context, issueURL string) ([]IssueTimeInfo, error) {
	// IssueのURLからowner, repo, issueNumberを抽出
	urlParts := strings.Split(issueURL, "/")
	if len(urlParts) < 7 {
		return nil, fmt.Errorf("invalid issue URL format: %s", issueURL)
	}

	owner := urlParts[3]
	repo := urlParts[4]
	issueNumber, err := strconv.Atoi(urlParts[6])
	if err != nil {
		return nil, fmt.Errorf("invalid issue number in URL: %s, error: %v", issueURL, err)
	}

	var allSubIssues []IssueTimeInfo
	cursor := ""

	// GraphQLクエリの準備
	query := `
    query GetSubIssues($owner: String!, $repo: String!, $issueNumber: Int!, $cursor: String) {
      repository(owner: $owner, name: $repo) {
        issue(number: $issueNumber) {
          title
          subIssues(first: 100, after: $cursor) {
            pageInfo {
              hasNextPage
              endCursor
            }
            edges {
              node {
                id
                number
                title
                state
                stateReason
                author {
                  login
                }
                labels(first: 100) {
                  nodes {
                    name
                  }
                }
                assignees(first: 10) {
                  nodes {
                    login
                  }
                }
                url
                createdAt
                closedAt
                repository {
                  name
                  owner {
                    login
                  }
                }
              }
            }
          }
        }
      }
    }`

	// ページネーションを使って全てのサブIssueを取得
	for {
		variables := map[string]interface{}{
			"owner":       owner,
			"repo":        repo,
			"issueNumber": issueNumber,
		}

		if cursor != "" {
			variables["cursor"] = cursor
		}

		var response SubIssueQueryResponse
		err := client.Execute(ctx, query, variables, &response)
		if err != nil {
			return nil, fmt.Errorf("executing GraphQL query for sub-issues: %w", err)
		}

		// 各サブIssueを処理
		for _, edge := range response.Repository.Issue.SubIssues.Edges {
			subIssue := edge.Node

			// 作成日時をパース
			createdAtUTC, err := time.Parse(time.RFC3339, subIssue.CreatedAt)
			if err != nil {
				log.Printf("Error parsing createdAt time for sub-issue #%d: %v", subIssue.Number, err)
				continue
			}
			// UTCからJSTへ変換
			createdAtJST := createdAtUTC.In(jst)

			// 閉じられた日時をパース
			var closedAt *time.Time
			if subIssue.ClosedAt != nil {
				parsedTimeUTC, err := time.Parse(time.RFC3339, *subIssue.ClosedAt)
				if err != nil {
					log.Printf("Error parsing closedAt time for sub-issue #%d: %v", subIssue.Number, err)
					continue
				}

				parsedTimeJST := parsedTimeUTC.In(jst)
				closedAt = &parsedTimeJST
			}

			// アサインされたユーザーの取得
			assignees := make([]string, 0, len(subIssue.Assignees.Nodes))
			for _, assignee := range subIssue.Assignees.Nodes {
				assignees = append(assignees, assignee.Login)
			}

			// ラベルの取得
			labels := make([]string, 0, len(subIssue.Labels.Nodes))
			for _, label := range subIssue.Labels.Nodes {
				labels = append(labels, label.Name)
			}

			// 状態理由の取得
			stateReason := ""
			if subIssue.StateReason != nil {
				stateReason = *subIssue.StateReason
			}

			// IssueTimeInfoの作成（カスタムフィールドは取得できないため初期値を設定）
			subIssueInfo := IssueTimeInfo{
				IssueURL:      subIssue.URL,
				Title:         subIssue.Title,
				Author:        subIssue.Author.Login,
				Assignees:     assignees,
				CreatedAt:     createdAtJST,
				ClosedAt:      closedAt,
				State:         subIssue.State,
				StateReason:   stateReason,
				EstimatedTime: -1.0, // サブIssueではカスタムフィールドは取得できないため初期値を設定
				ActualTime:    -1.0,
				Size:          -1.0,
				Labels:        labels,
				HasParent:     true, // サブIssueなので親が存在する
			}

			allSubIssues = append(allSubIssues, subIssueInfo)
		}

		// ページネーション処理
		if !response.Repository.Issue.SubIssues.PageInfo.HasNextPage {
			break
		}

		cursor = *response.Repository.Issue.SubIssues.PageInfo.EndCursor
	}

	return allSubIssues, nil
}

// fetchAllIssuesWithSubIssues は全てのトップレベルIssueとそれぞれのサブIssueを取得する
func fetchAllIssuesWithSubIssues(client *GraphQLClient, ctx context.Context, topLevelIssues []IssueTimeInfo) ([]TopLevelIssueWithSubIssues, error) {
	var result []TopLevelIssueWithSubIssues

	for _, topIssue := range topLevelIssues {
		log.Printf("Fetching sub-issues for issue #%s: %s", getIssueNumberFromURL(topIssue.IssueURL), topIssue.Title)

		subIssues, err := fetchSubIssuesForIssue(client, ctx, topIssue.IssueURL)
		if err != nil {
			log.Printf("Error fetching sub-issues for issue #%s: %v", getIssueNumberFromURL(topIssue.IssueURL), err)
			// エラーが発生しても処理を続行
			subIssues = []IssueTimeInfo{}
		}

		result = append(result, TopLevelIssueWithSubIssues{
			TopLevelIssue: topIssue,
			SubIssues:     subIssues,
		})
	}

	return result, nil
}

// getIssueNumberFromURL はIssueのURLからIssue番号を抽出する
func getIssueNumberFromURL(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "unknown"
}

// printIssuesWithSubIssues はトップレベルIssueとその子Issueを表示する
func printIssuesWithSubIssues(issuesWithSubs []TopLevelIssueWithSubIssues) {
	fmt.Printf("\n## Top-level Issues with Sub-Issues\n\n")

	if len(issuesWithSubs) == 0 {
		fmt.Println("No issues found.")
		return
	}

	for i, issueWithSubs := range issuesWithSubs {
		topIssue := issueWithSubs.TopLevelIssue

		// 見積時間と実績時間の表示
		estTime := "N/A"
		if topIssue.EstimatedTime >= 0 {
			estTime = fmt.Sprintf("%.1f", topIssue.EstimatedTime)
		}

		actTime := "N/A"
		if topIssue.ActualTime >= 0 {
			actTime = fmt.Sprintf("%.1f", topIssue.ActualTime)
		}

		size := "N/A"
		if topIssue.Size >= 0 {
			size = fmt.Sprintf("%.1f", topIssue.Size)
		}

		state := "OPEN"
		if topIssue.State == "CLOSED" {
			state = "CLOSED"
		}

		closedDate := "N/A"
		if topIssue.ClosedAt != nil {
			closedDate = topIssue.ClosedAt.Format("2006-01-02")
		}

		// トップレベルIssueの情報を表示
		fmt.Printf("%d. [%s] **%s** ([Issue #%s](%s))\n",
			i+1,
			state,
			topIssue.Title,
			getIssueNumberFromURL(topIssue.IssueURL),
			topIssue.IssueURL)
		fmt.Printf("   - Created: %s, Closed: %s\n",
			topIssue.CreatedAt.Format("2006-01-02"),
			closedDate)
		fmt.Printf("   - Estimated/Actual/Size: %s/%s/%s\n",
			estTime,
			actTime,
			size)
		fmt.Printf("   - Assignees: %s\n",
			strings.Join(topIssue.Assignees, ", "))

		// サブIssueの情報を表示
		if len(issueWithSubs.SubIssues) > 0 {
			fmt.Printf("   - Sub-Issues (%d):\n", len(issueWithSubs.SubIssues))

			for j, subIssue := range issueWithSubs.SubIssues {
				subState := "OPEN"
				if subIssue.State == "CLOSED" {
					subState = "CLOSED"
				}

				subClosedDate := "N/A"
				if subIssue.ClosedAt != nil {
					subClosedDate = subIssue.ClosedAt.Format("2006-01-02")
				}

				fmt.Printf("     %d.%d. [%s] %s ([Issue #%s](%s))\n",
					i+1,
					j+1,
					subState,
					subIssue.Title,
					getIssueNumberFromURL(subIssue.IssueURL),
					subIssue.IssueURL)
				fmt.Printf("         - Created: %s, Closed: %s\n",
					subIssue.CreatedAt.Format("2006-01-02"),
					subClosedDate)
				fmt.Printf("         - Assignees: %s\n",
					strings.Join(subIssue.Assignees, ", "))
			}
		} else {
			fmt.Printf("   - No Sub-Issues\n")
		}

		fmt.Println() // 空行を入れて見やすくする
	}
}

// sub-issueの統計情報を表示する関数
func printSubIssuesStatistics(issuesWithSubs []TopLevelIssueWithSubIssues) {
	fmt.Printf("\n## Sub-Issues Statistics\n\n")

	totalTopLevel := len(issuesWithSubs)
	totalSubIssues := 0
	topLevelWithSubs := 0

	for _, issueWithSubs := range issuesWithSubs {
		if len(issueWithSubs.SubIssues) > 0 {
			topLevelWithSubs++
			totalSubIssues += len(issueWithSubs.SubIssues)
		}
	}

	fmt.Printf("- Total top-level issues: %d\n", totalTopLevel)
	fmt.Printf("- Top-level issues with sub-issues: %d (%.1f%%)\n",
		topLevelWithSubs,
		float64(topLevelWithSubs)/float64(totalTopLevel)*100)
	fmt.Printf("- Total sub-issues: %d\n", totalSubIssues)
	fmt.Printf("- Average sub-issues per top-level issue: %.2f\n",
		float64(totalSubIssues)/float64(totalTopLevel))

	if topLevelWithSubs > 0 {
		fmt.Printf("- Average sub-issues per top-level issue (only those with sub-issues): %.2f\n",
			float64(totalSubIssues)/float64(topLevelWithSubs))
	}
}

// fetchSubIssuesRecursively は特定のIssueに紐づくサブIssueを再帰的に取得する
func fetchSubIssuesRecursively(client *GraphQLClient, ctx context.Context, issueURL string, depth int, maxDepth int) ([]IssueTimeInfo, error) {
	// 再帰の深さ制限をチェック
	if depth >= maxDepth {
		log.Printf("Reached maximum recursion depth (%d) for issue: %s", maxDepth, issueURL)
		return []IssueTimeInfo{}, nil
	}

	// IssueのURLからowner, repo, issueNumberを抽出
	urlParts := strings.Split(issueURL, "/")
	if len(urlParts) < 7 {
		return nil, fmt.Errorf("invalid issue URL format: %s", issueURL)
	}

	owner := urlParts[3]
	repo := urlParts[4]
	issueNumber, err := strconv.Atoi(urlParts[6])
	if err != nil {
		return nil, fmt.Errorf("invalid issue number in URL: %s, error: %v", issueURL, err)
	}

	var allSubIssues []IssueTimeInfo
	cursor := ""

	// GraphQLクエリの準備
	query := `
    query GetSubIssues($owner: String!, $repo: String!, $issueNumber: Int!, $cursor: String) {
      repository(owner: $owner, name: $repo) {
        issue(number: $issueNumber) {
          title
          subIssues(first: 100, after: $cursor) {
            pageInfo {
              hasNextPage
              endCursor
            }
            edges {
              node {
                id
                number
                title
                state
                stateReason
                author {
                  login
                }
                labels(first: 100) {
                  nodes {
                    name
                  }
                }
                assignees(first: 10) {
                  nodes {
                    login
                  }
                }
                url
                createdAt
                closedAt
                repository {
                  name
                  owner {
                    login
                  }
                }
                projectItems(first: 10) {
                  nodes {
                    project {
                      title
                      number
                    }
                    fieldValues(first: 50) {
                      nodes {
                        __typename
                        ... on ProjectV2ItemFieldNumberValue {
                          field {
                            ... on ProjectV2FieldCommon {
                              name
                            }
                          }
                          number
                        }
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    }`

	// ページネーションを使って全てのサブIssueを取得
	for {
		variables := map[string]interface{}{
			"owner":       owner,
			"repo":        repo,
			"issueNumber": issueNumber,
		}

		if cursor != "" {
			variables["cursor"] = cursor
		}

		var response SubIssueQueryResponse
		err := client.Execute(ctx, query, variables, &response)
		if err != nil {
			return nil, fmt.Errorf("executing GraphQL query for sub-issues: %w", err)
		}

		// 各サブIssueを処理
		for _, edge := range response.Repository.Issue.SubIssues.Edges {
			subIssue := edge.Node

			// 状態理由の取得
			stateReason := ""
			if subIssue.StateReason != nil {
				stateReason = *subIssue.StateReason
			}

			// フィルタリング: CLOSEDかつCOMPLETEDのみを対象とする
			if !(subIssue.State == "CLOSED" && stateReason == "COMPLETED") {
				log.Printf("Skipping sub-issue #%d with state %s and state reason %s",
					subIssue.Number, subIssue.State, stateReason)
				continue
			}

			// 作成日時をパース
			createdAtUTC, err := time.Parse(time.RFC3339, subIssue.CreatedAt)
			if err != nil {
				log.Printf("Error parsing createdAt time for sub-issue #%d: %v", subIssue.Number, err)
				continue
			}
			// UTCからJSTへ変換
			createdAtJST := createdAtUTC.In(jst)

			// 閉じられた日時をパース
			var closedAt *time.Time
			if subIssue.ClosedAt != nil {
				parsedTimeUTC, err := time.Parse(time.RFC3339, *subIssue.ClosedAt)
				if err != nil {
					log.Printf("Error parsing closedAt time for sub-issue #%d: %v", subIssue.Number, err)
					continue
				}

				parsedTimeJST := parsedTimeUTC.In(jst)
				closedAt = &parsedTimeJST
			}

			// アサインされたユーザーの取得
			assignees := make([]string, 0, len(subIssue.Assignees.Nodes))
			for _, assignee := range subIssue.Assignees.Nodes {
				assignees = append(assignees, assignee.Login)
			}

			// ラベルの取得
			labels := make([]string, 0, len(subIssue.Labels.Nodes))
			for _, label := range subIssue.Labels.Nodes {
				labels = append(labels, label.Name)
			}

			// カスタムフィールドの処理
			estimatedTime, actualTime, size := -1.0, -1.0, -1.0

			// プロジェクトのカスタムフィールドを取得
			if len(subIssue.ProjectItems.Nodes) > 0 {
				for _, projectItem := range subIssue.ProjectItems.Nodes {
					for _, fieldValue := range projectItem.FieldValues.Nodes {
						if fieldValue.TypeName == "ProjectV2ItemFieldNumberValue" {
							fieldName := fieldValue.Field.Name
							if fieldName == estimatedLabel && fieldValue.Number != nil {
								estimatedTime = *fieldValue.Number
							} else if fieldName == actualLabel && fieldValue.Number != nil {
								actualTime = *fieldValue.Number
							} else if fieldName == "Size" && fieldValue.Number != nil {
								size = *fieldValue.Number
							}
						}
					}
				}
			}

			// IssueTimeInfoの作成
			subIssueInfo := IssueTimeInfo{
				IssueURL:      subIssue.URL,
				Title:         subIssue.Title,
				Author:        subIssue.Author.Login,
				Assignees:     assignees,
				CreatedAt:     createdAtJST,
				ClosedAt:      closedAt,
				State:         subIssue.State,
				StateReason:   stateReason,
				EstimatedTime: estimatedTime,
				ActualTime:    actualTime,
				Size:          size,
				Labels:        labels,
				HasParent:     true,              // サブIssueなので親が存在する
				SubIssues:     []IssueTimeInfo{}, // 空の子Issueリストで初期化
			}

			// このサブIssueの子Issueを再帰的に取得
			log.Printf("Fetching sub-issues for sub-issue #%d at depth %d", subIssue.Number, depth+1)
			childIssues, err := fetchSubIssuesRecursively(client, ctx, subIssue.URL, depth+1, maxDepth)
			if err != nil {
				log.Printf("Warning: Error fetching sub-issues for issue #%d: %v", subIssue.Number, err)
			} else {
				subIssueInfo.SubIssues = childIssues
			}

			allSubIssues = append(allSubIssues, subIssueInfo)
		}

		// ページネーション処理
		if !response.Repository.Issue.SubIssues.PageInfo.HasNextPage {
			break
		}

		cursor = *response.Repository.Issue.SubIssues.PageInfo.EndCursor
	}

	return allSubIssues, nil
}

// enrichIssuesWithSubIssues はトップレベルIssueに再帰的にサブIssueを追加する
func enrichIssuesWithSubIssues(client *GraphQLClient, ctx context.Context, topLevelIssues []IssueTimeInfo, maxDepth int) ([]IssueTimeInfo, error) {
	enrichedIssues := make([]IssueTimeInfo, len(topLevelIssues))

	// 各トップレベルIssueに対して処理
	for i, topIssue := range topLevelIssues {
		log.Printf("Fetching sub-issues for top-level issue #%s: %s", getIssueNumberFromURL(topIssue.IssueURL), topIssue.Title)

		// 子Issueを再帰的に取得
		subIssues, err := fetchSubIssuesRecursively(client, ctx, topIssue.IssueURL, 0, maxDepth)
		if err != nil {
			log.Printf("Error fetching sub-issues for issue #%s: %v", getIssueNumberFromURL(topIssue.IssueURL), err)
			// エラーが発生しても処理を続行
		}

		// コピーしてサブIssueを設定
		enrichedIssues[i] = topIssue
		enrichedIssues[i].SubIssues = subIssues
	}

	return enrichedIssues, nil
}

// printIssueHierarchy はIssueの階層構造を再帰的に表示する (Markdown対応版)
func printIssueHierarchy(issues []IssueTimeInfo, prefix string, level int) {
	for _, issue := range issues {
		// インデント用のプレフィックス (Markdown用に修正)
		indentPrefix := strings.Repeat("    ", level)
		bulletChar := "*" // Markdownの箇条書き

		// Issueの基本情報を表示
		fmt.Printf("%s%s [%s] %s (#%s)\n",
			indentPrefix,
			bulletChar,
			issue.State,
			issue.Title,
			getIssueNumberFromURL(issue.IssueURL))

		// 詳細情報はさらにインデントして表示
		detailIndent := indentPrefix + "    "

		fmt.Printf("%s- Created: %s, Closed: %s\n",
			detailIndent,
			issue.CreatedAt.Format("2006-01-02"),
			issue.ClosedAt.Format("2006-01-02"))

		if level == 0 { // トップレベルIssueの場合のみ時間情報を表示
			estTime := "N/A"
			if issue.EstimatedTime >= 0 {
				estTime = fmt.Sprintf("%.1f", issue.EstimatedTime)
			}

			actTime := "N/A"
			if issue.ActualTime >= 0 {
				actTime = fmt.Sprintf("%.1f", issue.ActualTime)
			}

			size := "N/A"
			if issue.Size >= 0 {
				size = fmt.Sprintf("%.1f", issue.Size)
			}

			fmt.Printf("%s- Est/Act/Size: %s/%s/%s\n",
				detailIndent,
				estTime,
				actTime,
				size)
		}

		if len(issue.Assignees) > 0 {
			fmt.Printf("%s- Assignees: %s\n",
				detailIndent,
				strings.Join(issue.Assignees, ", "))
		}

		// 子Issueを再帰的に表示
		if len(issue.SubIssues) > 0 {
			printIssueHierarchy(issue.SubIssues, prefix, level+1)
		}
	}
}

// printIssuesWithHierarchy はトップレベルIssueとサブIssueの階層構造を表示する (Markdown対応版)
func printIssuesWithHierarchy(issues []IssueTimeInfo) {
	fmt.Printf("\n## Issue Hierarchy\n\n")

	if len(issues) == 0 {
		fmt.Println("No issues found.")
		return
	}

	for i, issue := range issues {
		fmt.Printf("%d. [%s] %s (#%s)\n",
			i+1,
			issue.State,
			issue.Title,
			getIssueNumberFromURL(issue.IssueURL))

		// 基本情報の表示
		closedDate := "N/A"
		if issue.ClosedAt != nil {
			closedDate = issue.ClosedAt.Format("2006-01-02")
		}

		estTime := "N/A"
		if issue.EstimatedTime >= 0 {
			estTime = fmt.Sprintf("%.1f", issue.EstimatedTime)
		}

		actTime := "N/A"
		if issue.ActualTime >= 0 {
			actTime = fmt.Sprintf("%.1f", issue.ActualTime)
		}

		size := "N/A"
		if issue.Size >= 0 {
			size = fmt.Sprintf("%.1f", issue.Size)
		}

		fmt.Printf("    - Created: %s, Closed: %s\n",
			issue.CreatedAt.Format("2006-01-02"),
			closedDate)
		fmt.Printf("    - Est/Act/Size: %s/%s/%s\n",
			estTime,
			actTime,
			size)

		if len(issue.Assignees) > 0 {
			fmt.Printf("    - Assignees: %s\n",
				strings.Join(issue.Assignees, ", "))
		}

		// 子Issueがあれば階層的に表示
		if len(issue.SubIssues) > 0 {
			printIssueHierarchy(issue.SubIssues, "", 1)
		}

		fmt.Println() // 空行を入れて見やすくする
	}
}

// calculateIssueHierarchyStats はIssue階層の統計情報を計算する
func calculateIssueHierarchyStats(issues []IssueTimeInfo) (int, int, map[int]int) {
	totalIssues := len(issues)
	totalSubIssues := 0
	depthCounts := make(map[int]int) // 深さごとのIssue数

	// 再帰的に統計を計算する内部関数
	var countRecursively func([]IssueTimeInfo, int) int
	countRecursively = func(issues []IssueTimeInfo, depth int) int {
		count := 0
		for _, issue := range issues {
			count++
			depthCounts[depth]++
			if len(issue.SubIssues) > 0 {
				count += countRecursively(issue.SubIssues, depth+1)
			}
		}
		return count
	}

	// 最初のレベルはカウント済み、子孫のみをカウント
	for _, issue := range issues {
		depthCounts[0]++
		if len(issue.SubIssues) > 0 {
			totalSubIssues += countRecursively(issue.SubIssues, 1)
		}
	}

	return totalIssues, totalSubIssues, depthCounts
}

// printIssueHierarchyStats はIssue階層の統計情報を表示する
func printIssueHierarchyStats(issues []IssueTimeInfo) {
	fmt.Printf("\n## Issue Hierarchy Statistics\n\n")

	topLevelCount, subIssueCount, depthCounts := calculateIssueHierarchyStats(issues)
	totalIssues := topLevelCount + subIssueCount

	fmt.Printf("- Total issues: %d\n", totalIssues)
	fmt.Printf("- Top-level issues: %d (%.1f%%)\n",
		topLevelCount,
		float64(topLevelCount)/float64(totalIssues)*100)
	fmt.Printf("- Sub-issues: %d (%.1f%%)\n",
		subIssueCount,
		float64(subIssueCount)/float64(totalIssues)*100)

	if topLevelCount > 0 {
		fmt.Printf("- Average sub-issues per top-level issue: %.2f\n",
			float64(subIssueCount)/float64(topLevelCount))
	}

	// 深さごとの統計
	fmt.Printf("\n### Issues by Depth\n\n")

	// キーをソートして深さ順に表示
	var depths []int
	for depth := range depthCounts {
		depths = append(depths, depth)
	}
	sort.Ints(depths)

	fmt.Printf("| %-12s | %-10s | %-8s |\n", "Depth", "Count", "Percent")
	fmt.Println("|--------------|------------|----------|")

	for _, depth := range depths {
		count := depthCounts[depth]
		fmt.Printf("| %-12s | %-10d | %-8.1f%% |\n",
			getDepthName(depth),
			count,
			float64(count)/float64(totalIssues)*100)
	}
}

// getDepthName は階層の深さに対応する名前を返す
func getDepthName(depth int) string {
	switch depth {
	case 0:
		return "Top-level"
	case 1:
		return "Children"
	case 2:
		return "Grandchildren"
	default:
		return fmt.Sprintf("Depth %d", depth)
	}
}

// containsLabelCaseInsensitive は大文字小文字を区別せずにラベルが含まれているかをチェックする
func containsLabelCaseInsensitive(labels []string, target string) bool {
	targetLower := strings.ToLower(target)
	for _, label := range labels {
		if strings.ToLower(label) == targetLower {
			return true
		}
	}
	return false
}

// calculateIssueSummaries はトップレベルIssueごとのサマリー情報を計算する
func calculateIssueSummaries(issues []IssueTimeInfo) []IssueSummary {
	var summaries []IssueSummary

	for _, issue := range issues {
		// 子孫Issueの見積・実績時間を再帰的に集計
		subIssueCount, totalEstimated, totalActual, violations := sumSubIssueTimeAndViolations(issue.SubIssues)

		// このIssue自体のルール違反をチェック
		selfViolations := checkIssueRuleViolation(issue)
		allViolations := append(selfViolations, violations...)

		// サマリー情報を作成
		summary := IssueSummary{
			IssueURL:         issue.IssueURL,
			Title:            issue.Title,
			Size:             issue.Size,
			TotalEstimated:   totalEstimated,
			TotalActual:      totalActual,
			SubIssueCount:    subIssueCount,
			HasRuleViolation: len(allViolations) > 0,
			Violations:       allViolations,
		}

		summaries = append(summaries, summary)
	}

	return summaries
}

// sumSubIssueTimeAndViolations は子孫Issueの見積・実績時間を再帰的に計算する
func sumSubIssueTimeAndViolations(subIssues []IssueTimeInfo) (int, float64, float64, []string) {
	count := len(subIssues)
	var totalEstimated, totalActual float64
	var allViolations []string

	for _, issue := range subIssues {
		// sbiまたはdev-sbiラベルを持つIssueのみ集計に含める
		hasSBI := containsLabelCaseInsensitive(issue.Labels, "sbi") || containsLabelCaseInsensitive(issue.Labels, "dev-sbi")

		if hasSBI {
			if issue.EstimatedTime >= 0 {
				totalEstimated += issue.EstimatedTime
			}

			if issue.ActualTime >= 0 {
				totalActual += issue.ActualTime
			}
		}

		// ルール違反チェック
		violations := checkIssueRuleViolation(issue)
		allViolations = append(allViolations, violations...)

		// 子孫Issueも再帰的に処理
		subCount, subEst, subAct, subViolations := sumSubIssueTimeAndViolations(issue.SubIssues)
		count += subCount
		totalEstimated += subEst
		totalActual += subAct
		allViolations = append(allViolations, subViolations...)
	}

	return count, totalEstimated, totalActual, allViolations
}

// checkIssueRuleViolation は単一Issueのルール違反をチェックする
func checkIssueRuleViolation(issue IssueTimeInfo) []string {
	var violations []string

	// PBIルールチェック
	hasPBI := containsLabelCaseInsensitive(issue.Labels, "pbi") || containsLabelCaseInsensitive(issue.Labels, "dev-pbi")
	if hasPBI && issue.Size < 0 {
		violations = append(violations, fmt.Sprintf("Issue #%s: pbi/dev-pbiラベルがありますがSizeが設定されていません",
			getIssueNumberFromURL(issue.IssueURL)))
	}

	// SBIルールチェック
	hasSBI := containsLabelCaseInsensitive(issue.Labels, "sbi") || containsLabelCaseInsensitive(issue.Labels, "dev-sbi")
	if hasSBI {
		var missingFields []string

		if issue.EstimatedTime < 0 {
			missingFields = append(missingFields, "見積時間")
		}

		if issue.ActualTime < 0 {
			missingFields = append(missingFields, "実績時間")
		}

		if len(missingFields) > 0 {
			violations = append(violations, fmt.Sprintf("Issue #%s: sbi/dev-sbiラベルがありますが%sが設定されていません",
				getIssueNumberFromURL(issue.IssueURL), strings.Join(missingFields, "と")))
		}

		// 難易度ラベルチェック
		hasDifficultyLabel := false
		difficultyLabels := []string{"difficulty:low", "difficulty:medium", "difficulty:high"}

		for _, label := range difficultyLabels {
			if containsLabelCaseInsensitive(issue.Labels, label) {
				hasDifficultyLabel = true
				break
			}
		}

		if !hasDifficultyLabel {
			violations = append(violations, fmt.Sprintf("Issue #%s: 難易度ラベル(difficulty:low/medium/high)が設定されていません",
				getIssueNumberFromURL(issue.IssueURL)))
		}
	}

	return violations
}

// printIssueSummaries はトップレベルIssueのサマリー情報を表示する
func printIssueSummaries(summaries []IssueSummary) {
	fmt.Printf("\n## トップレベルIssueのサマリー\n\n")

	if len(summaries) == 0 {
		fmt.Println("表示するIssueがありません。")
		return
	}

	// テーブルヘッダー
	fmt.Printf("| %-6s | %-40s | %-10s | %-15s | %-15s | %-10s | %-15s |\n",
		"Issue", "Title", "Size", "Est. Total (h)", "Act. Total (h)", "Sub Issues", "Ratio (A/E)")
	fmt.Println("|--------|------------------------------------------|------------|-----------------|-----------------|------------|-----------------|")

	// 全体の合計
	var totalSize, totalEstimated, totalActual float64
	var totalSubIssues int
	var issuesWithViolations int

	for _, summary := range summaries {
		// Issue番号を抽出
		issueNum := getIssueNumberFromURL(summary.IssueURL)

		// タイトルが長すぎる場合は切り詰める
		title := summary.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}

		// 数値フィールドの表示形式
		size := "N/A"
		if summary.Size >= 0 {
			size = fmt.Sprintf("%.1f", summary.Size)
			totalSize += summary.Size
		}

		estTotal := "N/A"
		if summary.TotalEstimated > 0 {
			estTotal = fmt.Sprintf("%.1f", summary.TotalEstimated)
			totalEstimated += summary.TotalEstimated
		}

		actTotal := "N/A"
		if summary.TotalActual > 0 {
			actTotal = fmt.Sprintf("%.1f", summary.TotalActual)
			totalActual += summary.TotalActual
		}

		// 比率の計算
		ratio := "N/A"
		if summary.TotalEstimated > 0 && summary.TotalActual > 0 {
			ratio = fmt.Sprintf("%.2f", summary.TotalActual/summary.TotalEstimated)
		}

		// 表の行を出力
		fmt.Printf("| %-6s | %-40s | %-10s | %-15s | %-15s | %-10d | %-15s |\n",
			issueNum, title, size, estTotal, actTotal, summary.SubIssueCount, ratio)

		totalSubIssues += summary.SubIssueCount

		if summary.HasRuleViolation {
			issuesWithViolations++
		}
	}

	// 合計行
	fmt.Println("|--------|------------------------------------------|------------|-----------------|-----------------|------------|-----------------|")
	fmt.Printf("| %-6s | %-40s | %-10.1f | %-15.1f | %-15.1f | %-10d | %-15s |\n",
		"合計", fmt.Sprintf("%d Issues (%d with violations)", len(summaries), issuesWithViolations),
		totalSize, totalEstimated, totalActual, totalSubIssues,
		fmt.Sprintf("%.2f", totalActual/totalEstimated))

	// 詳細情報
	fmt.Printf("\n### 詳細情報\n\n")

	for i, summary := range summaries {
		issueNum := getIssueNumberFromURL(summary.IssueURL)

		fmt.Printf("%d. **Issue #%s**: [%s](%s)\n",
			i+1, issueNum, summary.Title, summary.IssueURL)

		// サイズ情報
		if summary.Size >= 0 {
			fmt.Printf("   - Size: %.1f\n", summary.Size)
		} else {
			fmt.Printf("   - Size: N/A\n")
		}

		// 子孫Issue情報
		fmt.Printf("   - 子孫Issue数: %d\n", summary.SubIssueCount)

		// 時間情報
		if summary.TotalEstimated > 0 {
			fmt.Printf("   - 見積時間合計: %.1f 時間\n", summary.TotalEstimated)
		} else {
			fmt.Printf("   - 見積時間合計: N/A\n")
		}

		if summary.TotalActual > 0 {
			fmt.Printf("   - 実績時間合計: %.1f 時間\n", summary.TotalActual)
		} else {
			fmt.Printf("   - 実績時間合計: N/A\n")
		}

		if summary.TotalEstimated > 0 && summary.TotalActual > 0 {
			fmt.Printf("   - 実績/見積比率: %.2f\n", summary.TotalActual/summary.TotalEstimated)
		}

		// ルール違反の表示
		if summary.HasRuleViolation {
			fmt.Printf("   - **ルール違反あり**: %d 件\n", len(summary.Violations))
			for j, violation := range summary.Violations {
				fmt.Printf("     %d.%d. %s\n", i+1, j+1, violation)
			}
		}

		fmt.Println() // 空行を入れて見やすくする
	}
}
