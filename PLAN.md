# RSSHuBalance 计划

对外提供与 RSSHub 相同的路径，把请求路由到可动态增删的 RSSHub 实例池：失败可 fallback、失败率低、延迟低、不压垮实例。

## 决策

- Go，仅标准库；单进程，不做多副本
- 配置用环境变量；实例池存 JSON 状态文件；统计只在内存
- 本服务不缓存响应；客户端等待时长 = 上游请求上限（默认 60s），阅读器抓取超时需不低于此值

## TODO

- [x] 项目骨架：Go module、`cmd/rsshubalance`、Dockerfile；环境变量：监听地址（默认 `:1200`）、上游请求上限（60s）、最大尝试数（3）、管理 token、状态文件路径
- [x] 测试环境：真实 RSSHub 实例经 `RSSHUB_URLS` 提供给代理层测试（未配置的路由产生真实错误页，关闭的端口产生连接失败）
- [x] 实例池：字段 `url`、`max_concurrency`（默认 4）；启动时加载状态文件，变更时原子写回
- [x] 管理 API（Bearer token）
  - `GET /admin/instances`；`PUT /admin/instances` 按 `url` 新增或更新；`DELETE /admin/instances?url=`
  - `GET /admin/stats`：各实例及实例×命名空间的成功率、延迟
- [x] 代理入口：路径与查询原样转发；`/healthz` 本地响应；完整缓冲上游响应后再返回；透传 `Content-Type`；UA 为 `RSSHuBalance/<版本>`
- [x] 响应校验：2xx 且 XML 根元素为 `rss`/`feed`/`RDF`，或可解析的 JSON；空 feed 算成功
- [x] 错误分类（先用真实实例确认 RSSHub 错误页特征）
  - 实例级，记 `p_up` 失败：连接/DNS/TLS 错误、网关 502/504、非 RSSHub 产生的页面（如 Cloudflare 验证页）、403
  - 路由级，记 `p_up` 成功、`p_route` 失败：RSSHub 产生的非 feed 响应（带 `Access-Control-Allow-Methods: GET` 头或为 RSSHub 错误页）、404
  - 超时（达到上游上限）：记 `p_route` 失败；尝试期间该实例无任何成功响应时再记 `p_up` 失败
  - 429：不计成败，按 `Retry-After` 冷却（缺省 60s）
- [x] 统计模型（纯逻辑，注入时钟与随机源）
  - 成功率 p = `p_up`(实例) × `p_route`(实例, 路径)，均为按时间指数衰减计数的 Beta 分布，先验 Beta(1,1)
  - 半衰期：`p_up` 30min，`p_route` 24h
  - `p_route` 分层收缩：路径（不含查询）→ 命名空间（路径第一段）→ 实例整体 → 先验，伪计数 k=3
  - 每个（实例, 命名空间）维护成功延迟（对数均值/方差 EWMA）与失败延迟（EWMA），样本不足时用实例整体值
- [x] 排序（Thompson 采样）：每次请求对 `p_up`、`p_route` 各采样一次相乘得 p̃；c = p̃·成功延迟 + (1−p̃)·失败延迟；按 p̃/c 降序；跳过并发已满或冷却中的实例
- [x] 尝试调度（fallback 与对冲同一循环）
  - 发起首个候选；某次尝试失败 → 立即发起下一个
  - 距上次发起超过该实例在该命名空间成功延迟的 P90（exp(μ+1.28σ)）仍无结果 → 并行发起下一个；同时在途 ≤ 2；无延迟样本时不对冲
  - 总尝试数 ≤ 最大尝试数；候选全部满并发时排队
  - 首个合法响应返回客户端；其余尝试继续到上游上限，只用于更新统计
  - 全部失败：均为 404 返回 404，否则 502

## 未纳入

- 已决定不做：自适应并发、全局重试预算、独立熔断器/健康检查、路由级熔断、空 feed 跨实例比对、统计持久化、可视化面板、改写 feed 的 self link
- 待确认：响应缓存/stale-if-error/请求合并、实例 ACCESS_KEY、客户端访问控制
