# SSH 执行的 CodeQL 告警处置

Talus 的 SSH 执行功能允许已鉴权调用者在选中的 SSH 服务器上执行完整命令。HTTP 路由验证 JWT 或 API Key；API Key 需要 `servers:exec` scope，并通过目标服务器权限检查。`SSHService.runCommand` 的 `session.Run(command)` 是该功能的预期执行点。它不在 Talus 主机上执行本地 Shell。此处的 `go/command-injection` 告警经审查按预期功能处置。

## 仓库配置

`.github/workflows/codeql.yml` 只在 Go matrix 项追加官方 `codeql/go-queries:AlertSuppression.ql`，标准安全查询继续启用，JavaScript 和 Actions 扫描保持现有配置。

`backend/internal/service/ssh.go` 在执行点的正上一行单独写入 `// codeql[go/command-injection]`。它只覆盖紧接着的那一行和指定规则；移动执行点时必须一起检查注释的位置。前面的说明记录鉴权、scope、目标权限和远程执行语义。

分析输出保存到 `sarif-results/go.sarif`。Go job 的 `Verify the intended SSH suppression` 步骤确认该执行点仍有命令注入分析结果，并含 `kind: inSource` 的 suppression。缺少结果或抑制标记时，步骤失败并要求重新审查。它不删除 SARIF 结果，也不禁用命令注入规则。

## 首次 PR 如何处理

SARIF 的 `suppressions` 标记不会直接将 GitHub 的告警状态改为 `dismissed`，所以 PR 的 Go 分析与抑制验证通过后，CodeQL 汇总仍可能显示一条新告警。

1. 在 PR 的 CodeQL 检查中打开告警，确认规则为 `go/command-injection`，文件为 `backend/internal/service/ssh.go`，位置为 `session.Run(command)`。
2. 检查调用路由的鉴权、`servers:exec` 和目标服务器权限，确认仍符合上述执行语义。
3. 点击 **Dismiss alert**，选择 **Won't fix**，填写理由：`Intentional authenticated remote SSH shell execution. API keys require servers:exec and target-server access; this is not a local shell on the Talus host.`
4. 确认该条告警显示 `dismissed` 及理由，并重新查看 PR 检查结果。若检查页面未更新，可重新运行 CodeQL 工作流。

需要当前账号具有仓库 code-scanning 告警处置权限。此操作仅处置已审查的这一条告警；其他命令注入告警仍需要正常审查。

## 合并后的自动处置与验收

在 `main` 的 Go 分析完成、SARIF 上传处理完成后，固定版本的 `advanced-security/dismiss-alerts` 读取抑制标记，通过 GitHub API 将仍然开放的对应告警标为 `dismissed`，理由为 `won't fix`，备注为 `Suppressed via SARIF`。已在首次 PR 中手动处置的告警保留人工理由和备注，Action 不会覆写。移除注释后，后续默认分支扫描会重新打开由此 Action 处置的告警。手动处置的告警不会因移除注释而被这个 Action 自动重新打开，需要另行复核。

该步骤只在 `refs/heads/main` 运行，因为告警的处置状态影响所有分支。在 PR 或功能分支上执行会提前改动默认分支的告警状态。

合并后检查 CodeQL 的 Go job 中抑制验证与自动处置步骤，并在仓库 **Security → Code scanning** 确认该条告警的状态、理由及备注：自动处置应有上述固定备注，首次手动处置应保留已审核的人工说明。不能仅凭 Action 显示绿色判断告警已处置；有权限的账号也可以用 GitHub CLI 查询：

```sh
gh api --paginate 'repos/molicherry/Talus/code-scanning/alerts?tool_name=CodeQL&state=dismissed&per_page=100' \
  --jq '.[] | select(.rule.id == "go/command-injection" and .most_recent_instance.location.path == "backend/internal/service/ssh.go") | {number, state, dismissed_reason, dismissed_comment, html_url, location: .most_recent_instance.location}'
```

对照 SARIF 中的执行点位置确认结果。若返回为空，查看自动处置步骤日志以及 `security-events: write` 权限，不能把“验证了 SARIF”当作“GitHub 告警已经关闭”。

参考：[官方 Go 抑制查询](https://github.com/github/codeql/blob/main/go/ql/src/AlertSuppression.ql)、[注释覆盖范围](https://github.com/github/codeql/blob/main/shared/util/codeql/util/suppression/AlertSuppression.qll)、[自动处置机制与限制](https://github.com/advanced-security/dismiss-alerts#features-and-limitations)。
