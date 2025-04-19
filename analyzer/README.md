GitHubの複数のリポジトリにまたがるIssueの処理プログラムをGOでGraphQL APIを用いて作成する。
Issueは以下の特徴を持つ。
1. GitHub Projectsによってカスタムフィールドが設定されている
   1. カスタムフィールドは以下の通り
      1. Size（数値型）
      2. 見積時間（数値型）
      3. 実績時間（数値型）
2. 以下のラベルに注目する
   1. pbi
   2. dev-pbi
   3. sbi
   4. dev-sbi
   5. Difficulty:Low
   6. Difficulty:Middle
   7. Difficulty:High
3. 以下の法則がある
   1. pbiまたはdev-pbiが設定されている場合、Sizeが設定される
   2. sbiまたはdev-sbiが設定されている場合、以下が設定される
      1. 見積時間
      2. 実績時間
      3. Difficulty:Low、Difficulty:Middle、Difficulty:Highのいずれか
   3. IssueはGitHubのSub-Issue機能を用いて構造化されている
      1. 親を持たないIssue（ルートIssueと呼ぶ）はpbi、またはdev-pbiが設定される
      2. 1のSub-Issueとしてpbi、dev-pbiを含む他のラベルを持つIssueが存在する
      3. Sub-Issueはさらに子孫のIssueを持つ場合がある
      4. 1つのルートIssueとその全ての子孫のIssueを合わせて家族と呼ぶ
4. 3のルールを破って値を設定していないIssueが存在する
5. プログラムは以下の処理を行う
   1. 指定した期間内にCOMPLETEDでクローズしたラベルがpbiまたはdev-biのIssueを抽出し保存する
   2. 保存したIssueから、ルートIssueを抽出する（ルートIssueは親Issueを持たない）
   3. ルートIssueごとの子孫のIssueを抽出する
   4. ルートIssueのクローズした理由ごとに、分類し以下を表示する
      1. ルートIssueのSize
      2. 子孫のsbiまたはdev-sbiラベルを付与されているIssueからDifficultyの種類ごとに合計した見積時間と実績時間
   5. ルールを破っているIssueのURLをAsignee（Asigneeが設定されていない場合はIssueの作成者）を列挙する
6. 必要なパラメータは環境変数、または.envファイルから取得する
   1. GITHUB_TOKEN
   2. ORG
   3. REPOS
   4. PROJECT
   5. START_DATE
   6. END_DATE
-----------------
GitHubのGraphQL APIを使用してIssueを抽出し、処理を行うGOのプログラムを段階的に作成する。
Issueたちの多くはSub-Issue機能を用いて構造化されている。
親Issueを持たないIssueをルートIssueと呼ぶ。
1つの親Issueの子孫のSub-Issueたちを全て含むIssueの集合を家族と呼ぶ。
パラメータとなる変数は環境変数または.envファイルで与える。
GitHubの組織をORGで、対象となるリポジトリたちをREPOSで与える。
ステップ1
指定された期間内でクローズしたIssueを抽出する。
ステップ2
ステップ1で抽出したIssueからルートIssueを探し出すプログラムを作成する。
