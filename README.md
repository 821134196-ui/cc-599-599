# 报刊亭期刊订阅 · 到刊与缺刊补发系统

逐期号记录订阅履约情况：到刊登记并自动分配订户、领取签收、缺刊补发（出版社回执本地模拟）、停刊退款。
后端 Go + Gin + GORM + SQLite，前端 Vue 3 + Vite，前端构建产物由 Go 二进制嵌入托管。

## 一条命令启动

```bash
./run.sh            # 自动 npm install、构建前端、go run 启动，访问 http://localhost:8080
./run.sh --dev      # 前端热更新开发模式（:5173，API 代理到 :8080）
```

环境变量：`KIOSK_ADDR`（默认 `:8080`）、`KIOSK_DB`（默认 `kiosk.db`）。

首次启动会自动播种演示数据：《万象月刊》15.00 元/期，签约规则固定
**2026 年第 7/8 期合刊，按 2 期折算**；读者张晓明（全年 12 期）、李雅（半年 6 期）。

## 核心业务规则

| 规则 | 实现 |
|---|---|
| 逐期号履约，而非整份订阅完成与否 | 每份订阅生成 `fulfillment_items`，每期一行状态：未到货 / 已到刊待取 / 已签收 / 缺刊 / 待补发 / 已退款 |
| 合刊折算 | 合刊只有**一条期号记录**（如 `2026-07-08`），`credit=2`；总折算期数 = Σ credit（12 期订阅 → 11 条期号、12 个折算期数） |
| 到货登记后分配订户 | 正常到货只分配 `pending` 记录；补发批次只分配 `missing/reissuing` 记录；状态守卫 + 条件 UPDATE 保证每份只被分配一次 |
| 缺刊补发 | 缺刊按 期号+期刊 幂等申请；出版社受理后补发件独立批次入库，**归入原期号**（`is_reissue=true`），不增加订阅期数、不重复计交付 |
| 停刊退款 | 未签收期一律未履约，按折算期数 × 签约单期价退款，逐期号出明细；已到店未取份释放回可发库存 |
| 取刊方式调整 | 只更新 `pending` 的未来刊期；已到货/已签收记录保持原样，变更历史留痕 |
| 幂等 | 到货回执号唯一索引；签收状态守卫 + 签收回执唯一；补发申请键 `M<期刊>:<期号>`；补发出版社回执确定性编号 `RE-<期刊代码>-<期号>`；停刊退款单每订阅唯一 |

## 验证（期数 / 库存 / 金额对账）

```bash
go test ./... -v
```

四个场景：

1. **合刊** `TestMergedIssueCredits`：12 折算期 → 11 条期号、合刊 credit=2、全签收后履约期数=12 且合刊交付记录只有 1 条。
2. **重复回执** `TestDuplicateReceiptsIdempotent`：重复到货（同回执）库存/分配不变；重复签收已履约期数仍为 1；重复缺刊申请、重复出版社回执只产生 1 个补发批次；补发签收后总期数仍 12、不重复计交付。
3. **停刊退款** `TestStopRefundAccounting`：01/02 已签收、03 到店未取、04 补发件到店未取、其余未到货 → 未履约 **10 期**（含合刊 2）→ 应退 **¥150.00**，明细 9 行汇总与总额一致；释放 **2** 份库存占用；库存总账 到货 4 / 已分配 2 / 可发 2；重复停刊不重复生成退款单。
4. **取刊方式** `TestPickupModeFutureOnly`：已签收/已到货期保持原方式，未来期（含合刊）变为新方式。

## 主要 API

```
POST /api/readers            POST /api/magazines
POST /api/subscriptions                      {reader_id, magazine_id, start_issue_no, total_issues, pickup_mode}
GET  /api/subscriptions/:id                  逐期号明细 + 已履约折算期数 + 退款单
POST /api/subscriptions/:id/pickup-mode      {mode}           仅未来刊期
POST /api/subscriptions/:id/stop             停刊退款（含明细、释放库存数）
POST /api/arrivals           {magazine_id, issue_no, quantity, publisher_receipt, kind}
GET  /api/stock              批次级库存：到货/已分配/可发
POST /api/missing            {subscription_id, issue_no}
POST /api/publisher/respond  {magazine_id, issue_no, accept}  本地模拟出版社回执
POST /api/receive            {subscription_id, issue_no, receipt}
```

所有重复提交均返回 `duplicated: true` 且不再改动任何计数。
