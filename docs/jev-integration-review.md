# Jev / System One 接入核验

核验日期：2026-11-03。协议依据：TypeSafe 官方 `https://api.typesafe.ai/openapi.json`（OpenAPI 3.1.0，API schema 0.2.0）。

## 请求与配置

- 使用 Bearer API Key，通过 `POST /v1/systemone` 提交 `model`、`state` 和命名的 `questions`。拦截裁决使用 `choice` 问题及 `allow / ask / deny` criteria。
- `GET /v1/models` 返回 `models[].name`，不是 OpenAI 的 `data[].id`。模型管理页可加载账号可用模型，默认别名为 `jev-latest`。
- Base URL 可填服务根地址、`/v1` 或完整 `/v1/systemone` 地址；只接受 HTTP(S)，不接受查询参数和片段。
- 使用配置中的独立代理；空代理直连，不继承进程代理环境。Jev 配置仅可用于拦截裁判，不可设为聊天或任务模型。

## 裁决与失败处理

- 校验 `answers.decision` 的类型、裁决值、必填置信度和三个概率字段。置信度及概率必须是 0–1 的有限数字；无效响应进入现有重试/失败策略，不作为有效放行结果。
- 低于配置阈值的结果转人工审批；证据不完整或截断时，Jev 的 `allow` 转为 `ask`。
- HTTP 错误、格式错误及取消/超时作为调用失败；生产调用遵守裁判单次超时和重试配置。连接测试及模型列表查询限时 30 秒。
- 记账使用官方 `usage.input_tokens / output_tokens`；审查记录保留输入和配置摘要。

## 验证范围

自动测试覆盖模型列表原生协议、代理、URL 规范化、正常裁决、异常响应、低置信度、证据截断、使用量和管理接口接线；前端类型检查、静态导出和现有前端 API 回归测试亦纳入发布验证。

**未使用真实 Jev API Key 调用生产服务。** 虚拟机没有保存 Jev profile，当前启用的裁判为其他模型。协议与模拟服务测试不能证明实际账号权限、余额、服务可达性或模型判定准确率；上线使用前仍应保存 Jev 配置并执行连接测试和业务样本验收。

本次新版本包含 Jev 功能及上述修复。历史 `v0.3.25` 不含 Jev 接入；将虚拟机恢复到该版本不会部署 Jev 功能。
