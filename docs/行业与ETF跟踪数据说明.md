# 行业 / ETF跟踪标的 数据获取逻辑

本文说明两个缓存库的数据来源、抓取方式、入库与预热逻辑：

- `data/database/industry.db` —— 股票所属行业（申万一级/二级）
- `data/database/etf_track.db` —— ETF 跟踪标的指数

两者结构几乎一致（主表 + `*_failed` 表），都是**"懒加载缓存 + 后台预热"**模式，且**共用一把预热锁**（串行执行，防限流）。

---

## 一、`industry.db`（股票行业）

### 1.1 表结构

**`industry`（主表，缓存）**
| 字段 | 说明 |
|---|---|
| code | 6 位股票代码（主键） |
| name | 股票名称 |
| industry | 行业（优先取二级，空则一级） |
| industry1 | 申万一级行业 |
| industry2 | 申万二级行业 |
| source | 来源，固定 `ths` |
| updated_at | 更新时间 |

**`industry_failed`（失败记录）**
| 字段 | 说明 |
|---|---|
| code | 代码（主键） |
| fails | 连续失败次数 |
| last_fail | 最后失败时间 |
| last_error | 最后错误信息（截断 200 字） |

### 1.2 数据来源

**同花顺 F10**：`https://basic.10jqka.com.cn/{code}/company.html`

- 从 HTML 里正则提取：`行业：</strong><span>...</span>`（如 `银行—股份制银行`）。
- **GBK 编码**，需用 `simplifiedchinese.GBK` 解码后再解析。
- 名称取 `<title>名称(...)`。
- 按 `—` 拆分：`—` 前为一级，后为二级。
- 请求头：`User-Agent` + `Referer: https://basic.10jqka.com.cn`。

### 1.3 获取流程

```text
┌─────────────────────────────────────────────────────────┐
│ 通道A：接口实时抓取（缓存 miss 时）                        │
│  GET /api/industry?code=600519                            │
│    → 查 industry 表命中 → 直接返回                        │
│    → miss → industryFetchSingle()                         │
│              → industryFetchTHS()（抓同花顺F10）           │
│              → industrySaveSingle()（INSERT OR REPLACE）  │
│              → 返回                                        │
└─────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────┐
│ 通道B：后台预热（启动后 1 分钟首次，之后每 24 小时）        │
│  industryWarmLoop()                                       │
│    → warmMu.Lock()          ← 与 ETF 预热共用同一把锁      │
│    → industryMissingCodes() 全市场股票 - 已缓存 - 近期失败 │
│    → 逐只 industryFetchSingle()，每只间隔 3 秒            │
│    → 连续失败 5 次 → 暂停 5 分钟                          │
│    → 每 5 只打一次进度日志                                │
│    → warmMu.Unlock()                                     │
└─────────────────────────────────────────────────────────┘
```

### 1.4 缺失判断与失败退避

- `industryMissingCodes()`：全市场代码里 `IsStock()==true`（沪6/深0、30/北8、43、92）**且** `industry` 表未缓存**且**未处于失败退避期的。
- `industrySkipFailed()`：**连续失败 ≥3 次且 30 天内**不再重试（避免反复抓取限流）。
- `industryMarkResult()`：成功 → 删除失败记录；失败 → 累加次数。

---

## 二、`etf_track.db`（ETF 跟踪标的）

### 2.1 表结构

**`etf_track`（主表，缓存）**
| 字段 | 说明 |
|---|---|
| code | 6 位基金代码（主键） |
| name | 基金名称 |
| market | `sh` / `sz` |
| track_index | 跟踪标的指数（如 `沪深300`） |
| updated_at | 更新时间 |

**`etf_track_failed`（失败记录）**：同 `industry_failed`。

### 2.2 数据来源

**东财基金 F10**：`http://fundf10.eastmoney.com/jbgk_{code}.html`

- 正则提取：`跟踪标的</th><td...>...</td>`。
- 名称/市场**不从东财取**，而是回查 `codes.db`（`etfCodeModel()`）。
- 无跟踪标的数据（如未上市占位代码）→ 返回 `errTrackNotFound`（非网络错误，不触发熔断）。

### 2.3 获取流程

```text
┌─────────────────────────────────────────────────────────┐
│ 通道A：接口实时抓取（缓存 miss 时）                        │
│  GET /api/etf-track?code=510300                           │
│    → 查 etf_track 表命中 → 返回                           │
│    → miss → fetchEtfTrack()（抓东财F10）                  │
│              → INSERT OR REPLACE 入库                     │
│    → market 为空时回填 codes.db 的名称/市场               │
└─────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────┐
│ 通道B：后台预热（启动后 90 秒首次，之后每 24 小时）         │
│  etfTrackWarmLoop()                                       │
│    → warmMu.Lock()          ← 与行业预热共用同一把锁      │
│    → etfTrackMissingCodes()  全市场ETF - 已缓存 - 近期失败 │
│    → 逐只 fetchEtfTrack()，每只间隔 2.5 秒               │
│    → 连续失败 5 次 → 暂停 5 分钟                         │
│    → 每 5 只打一次进度日志                                │
│    → warmMu.Unlock()                                     │
└─────────────────────────────────────────────────────────┘
```

### 2.4 缺失判断

- `etfTrackMissingCodes()`：全市场代码里 `IsETF()==true`（沪51/52/56/58、深15/16）**且** `etf_track` 表未缓存**且**未处于失败退避期的。
- 失败退避逻辑同行业（≥3 次 + 30 天）。

---

## 三、关键设计点与注意事项

### 3.1 两库共用的东西

| 共用项 | 说明 |
|---|---|
| 预热锁 `warmMu` | `industry.go` 定义，行业与 ETF 预热**互斥串行** |
| 失败退避策略 | 连续失败 ≥3 次 → 30 天内不再重试 |
| 熔断策略 | 连续失败 ≥5 次 → 暂停 5 分钟 |
| `truncateErr` | 错误信息截断 200 字 |

### 3.2 ⚠️ 串行导致 ETF 滞后（重要）

- 行业预热：约 5000 只 × 3 秒 ≈ **数小时**（增量时更短）。
- ETF 预热：约 2268 只 × 2.5 秒 ≈ **95 分钟**。
- 两条线**共用 `warmMu`**，ETF 必须等行业**全部跑完**才轮到。
- **后果**：若行业预热耗时长，`etf_track` 表会长时间为空（接口 miss 时才实时单抓）。
- **实测（2026-09-11）**：容器重启后行业预热从 0 补 921 只（约 46 分钟），期间 `etf_track` 表 0 行。
- **数据不丢**：成功后逐条 `INSERT OR REPLACE` 落盘，重启后**已完成部分不重来**（只补缺失）。

### 3.3 与除权排期（`ex_calendar`）的关系

- **ETF 除权排期不依赖 `etf_track`**：只需 `codes.db` 里的 ETF 代码去走 `0x000f`。
- `etf_track` 仅服务于 `/api/etf-track`（查"ETF→跟踪指数"映射）。

### 3.4 相关接口

| 接口 | 说明 |
|---|---|
| `GET /api/industry?code=600519` | 查股票行业（支持批量逗号分隔，miss 实时抓） |
| `GET /api/industry/codes` | 已缓存行业的代码列表 |
| `GET /api/etf-track?code=510300` | 查 ETF 跟踪标的（支持批量，miss 实时抓） |

### 3.5 相关文件

| 文件 | 内容 |
|---|---|
| `web/industry.go` | 行业表+抓取器+预热循环+接口，`warmMu` 定义处 |
| `web/etf_track.go` | ETF跟踪表+抓取器+预热循环+接口 |
| `web/server.go` | `InitIndustry()`、`InitEtfTrack()` 启动调用 |
