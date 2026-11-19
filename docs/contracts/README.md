# v1 合同与合成样例

JSON Schema 使用 Draft 2020-12。外部消费者按 `schema_version` 选择字段，忽略不认识的新增字段。Schema 严格校验发布者当前的字段白名单，验证器启用 format 检查。产品行为见 [接口详设](../design/04-data-integration.md)。

| 合同 | 清晰语义 | 示例 |
| --- | --- | --- |
| [metrics-v1](metrics-v1.schema.json) | Windows 终端只读指标快照。`quality=ok` 时 `value` 为数值，其他质量明示 `null`。时间范围和观测点整体携带，不从不同会话合并 | [样例](sample-metrics.json) |
| [incident-v1](incident-v1.schema.json) | HTTPS 上报的单个异常/恢复，NDJSON 每行也用同一格式。`event_id` 是幂等键。`incident_id` 串起打开和恢复。`test_only=true` 只用于独立的已确认测试，不参与实际告警 | [样例](sample-incident.json) |
| [analysis-report-v1](analysis-report-v1.schema.json) | 模型的报告结果。配对请求、内容范围、证据和动作由宿主再次验证，不出现在跨机遥测。建议只允许已登记的只读或预览入口 | [样例](sample-analysis-report.json) |

消费者以 `metric_id` 选取口径。`scope_id`、`target_alias` 和 `device_alias` 是面向导出的伪名，不允许从真实文件路径、用户名和明文地址直接生成。`network.*` 都带协议和目标别名，由消费者在同组内比较。`disk.*` 按卷统计时用卷别名，无法可靠区别时记录为机器总量。测试接收器以 HTTP 2xx 表示接受，重复事件应返回同类接受状态。

报告允许的引用均来自请求的证据集合。`volume_id`、`target_id` 只是已校验的内部引用。实际清理动作审批、写盘、备份和系统账户权限由主进程处理，模型报告无法提供这些授权。`summary` 与 `limitations` 为纯文本。未满足复杂 schema 的提供商降级为本地规则或经过验证的纯文本显示。
