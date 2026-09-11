# 除权除息发现方案：TDX `0x000f` 原生接口（替代 THS/东财高频爬取）

> 状态：已实现（无缓存版，2026-09-11实测通过），缓存/预热暂缓
> 相关代码：`protocol/const.go`（`TypeXdxr=0x000f`）、`protocol/model_xdxr.go`（新建）、`client.go`（`GetXdxrInfo`）、`web/xdxr.go`（新建，`GET /api/xdxr`）、`web/server.go`（路由注册）

## 1. 背景：为什么需要这个

第三方系统现状（已确认）：

- 全量：`GET /api/kline-all/ths?code={code}&type=day`（同花顺前复权 QFQ）
- 增量：`GET /api/kline-all?code={code}&type=day&limit=1`（实际等于 `/api/kline-all/tdx`，通达信不复权 BFQ）

问题链：

1. 前复权以最新价为锚，**除权日一到，历史全部重写**。只 `append` 今天修不好昨天和以前（例：10派10、20元的票，除权后 `T-1` 的 QFQ 应从 20 改成 19，不重拉则永久偏 5%）。
2. 全量 QFQ + 增量 BFQ 是杂交写法：平时最新一天 `BFQ==QFQ` 碰巧对，除权必错。
3. 想改成“发现哪只变了 → 只对它 `ths` 全量删了重插”，但**现有接口做不了低成本发现**：
   - `/api/kline-all/ths?limit=N` 服务端还是拉同花顺 `all.js` 全量再截（`server_api_extended.go:1221 getQfqKlineDay`），2~8秒/只，无缓存（不像 `industry/etf-track` 有 SQLite），全市场每天轮询必触发限流。
   - `/api/kline-all?limit=N` 服务端还是 `GetKlineDayAll` 全量拼接再截（`server_api_extended.go:1154`），省不了时间。
   - `/api/kline-history?type=day/week/month` 同样走 `getQfqKlineDay` 全量（`server_api_extended.go:111`），只有分钟/小时是真增量。
4. 东财同样有频率风控，不能作为高频发现源。

结论：**发现阶段必须脱离 HTTP 爬虫**，否则 THS/东财二选一都会被封。

## 2. 方案选型

| 方案 | 通道 | 限流面 | 评价 |
|---|---|---|---|
| A. TDX `0x000f` 除权除息（推荐） | 已有 TDX 7709 TCP 连接池 | 与 K 线同一通道，实测不封 IP | 单股历史明细，含分红/送转/配股，可缓存，改动小 |
| B. Tushare `adj_factor`（如 `lidai0117/zer0share`） | Tushare Pro HTTP | 要 token，按积分计，不适合高频轮询但适合每天一次 | 因子变了=除权了，需引入外部依赖和 token |
| C. 订阅现成日历（如 `toontong/stock-dividend-calendar` ICS） | GitHub Pages/CalDAV 订阅 | 零爬取 | 只有未来排期、无历史明细，仅做提醒 |
| D. 继续 THS/东财高频轮询 | HTTP | 同花顺 429/封 IP，东财 `datacenter/push2` 有风控 | 否决 |

选 A。GitHub 依据：

- `rainx/pytdx`：`pytdx/parser/get_xdxr_info.py`、`hq.py:get_xdxr_info(market, code)`，Issue #8（`600300` 与券商软件对账）、Issue #37（权息字段含义+示例）。
- `handsomejustin/easy_tdx`：`src/easy_tdx/commands/xdxr_info.py`，最干净的实现，还修复了 pytdx 循环内误读 `body[:7]` 的 bug。
- `electkismet/eltdx`：`docs/methods/7709-除权除息整理.md`、`7709-股本变迁GBBQ.md`，`client.get_xdxr(code)` / `corporate.capital_changes(code)`，默认缓存，`refresh=True` 重拉。
- 侧证：`simonlin1212/a-stock-data` 明确把 `mootdx（TCP 7709）` 标为不封 IP 首选，东财标为“中有风控会封 IP”，所有东财调用统一走 `em_get()` 串行限流。

## 3. `0x000f` 协议要点（移植用）

以下综合上述三个仓库整理，Go 实现时以 `easy_tdx` 为准、`pytdx` 对账、`eltdx` 文档定字段名：

- 请求：`market(1B) + code(6s)`，`easy_tdx` 请求头为 `0c1f18760001 0b000b000f000100` + `struct.pack("<B6s", market, code)`。
- 响应：跳过 9 字节头 → `num(<H)` → 逐条：
  - `market(1B) + code(6s)`（注意必须从当前 `pos` 读，这是 pytdx 曾踩的坑）→ 跳 1 未知字节 →
  - 日期（`get_datetime`，年/月/日）→ `category(1B)` → 16 字节 chunk。
- `category` 口径（pytdx Issue #37 / eltdx GBBQ）：
  - `1` 除权除息 → chunk 按 `<ffff` 解：`分红(fenhong)、配股价(peigujia)、送转股(songzhuangu)、配股(peigu)`，日常只用这个。
  - `2/3/5/6/7/8/9/10` 股本类（送配股上市、股本变化、增发、回购等）→ 股数按通达信自定义浮点解码后再 `×10000`。
  - `11/12` 扩缩股 → float32 比例；`13/14` 权证 → `行权价/份数`；`15` 重整调整。首版可只透传 raw。
- 与本项目映射：`protocol/` 下仿 `model_kline.go` 建 `model_xdxr.go`（`XdxrReq.Bytes()` / `XdxrResp.Decode()`），`const.go` 启用 `TypeXdxr = 0x000f`，`client.go:handlerDealMessage` 加 `case protocol.TypeXdxr` 分支，`Manage/Pool` 复用现有连接。

## 4. 本项目实现步骤

1. **协议层** `protocol/model_xdxr.go`（新建）+ `protocol/const.go`（启用 `0x000f`）：
   - `XdxrReq{Exchange, Code}`、`XdxrRecord{Date, Category, CategoryName, Fenhong, Peigujia, Songzhuangu, Peigu, Raw}`。
   - 单测用 `600300/000001` 与 pytdx Issue #8/#37 的贴文对账（日期+分红+送转能对上即过）。
2. **客户端层** `client.go`：
   - `func (c *Client) GetXdxrInfo(code string) (*XdxrResp, error)`，走 `SendFrame`，market 由 `protocol.AddPrefix` 推导（沿用 `GetKline` 的做法）。
3. **服务层** `web/`（仿 `industry.go` 缓存范本）：
   - `GET /api/xdxr?code=600519`（单股，首版）→ 预留批量 `code=600519,000001`（逗号分隔，同 `industry`）。
   - SQLite：`data/database/xdxr.db`，表 `xdxr(code, date, category, fenhong, peigujia, songzhuangu, peigu, raw, updated_at)`，主键 `(code, date, category)`；历史只增不改，永久缓存；失败表仿 `industry_failed` 做退避。
   - 后续可选：`GET /api/xdxr/check?code=&date=`（判断指定日期后有无新增 `category==1`）、真增量 `GET /api/kline-latest?code=&count=2`（底层 `GetKlineDay(code,0,N)`，不拼全量，给 TDX 预筛用）。
4. **联调验证**：`000001/600300/600519` 三只对账 → 全市场小批量（100只）压测 TDX 通道 → 观察 `ths` 调用量是否从 N/天降到嫌疑名单量级。

非目标（首版不做）：全市场除权日历推送、配股/权证精细解码、周/月 QFQ 自动重算。

## 5. 新接口契约（草案）

```text
GET /api/xdxr?code=600519
GET /api/xdxr?code=600519,000001
```

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "count": 1,
    "list": [
      {
        "code": "600519",
        "records": [
          {"date": "2024-06-18", "category": 1, "category_name": "除权除息", "fenhong": 30.876, "peigujia": 0, "songzhuangu": 0, "peigu": 0}
        ]
      }
    ],
    "not_found": []
  }
}
```

字段口径：`fenhong/peigujia/songzhuangu/peigu` 均为**每10股**口径（服务器 float32 原值，如茅台 2024-06-19 `fenhong=308.76` 即10派308.76元；JSON 可能带 float32 尾差如 `308.760009765625`，调用方按需保留2位小数），`date` 为除权除息日（`YYYY-MM-DD`）。

## 6. 第三方调用流程（改造后，低频）

```python
# 每天收盘后
for code in 持仓或全市场:
    r = GET /api/xdxr?code={code}          # 走缓存，毫秒级，不碰THS
    if 有 category==1 且 date in [昨天, 今天]:
        GET /api/kline-all/ths?code={code}&type=day   # 仅嫌疑股，全量删了重插
        重算该股周/月线
    else:
        正常增量（QFQ表用ths今天条，BFQ表用tdx今天条，源别混）
```

效果：THS 全量从 `全市场/天` 降到 `嫌疑名单（通常<20只）/天`。

## 7. 风险

- `0x000f` 无官方文档，靠开源实现反推，需用 `600300/000001` 对账锁定 `category==1` 的四浮点口径；`11~15` 首版只存 raw，不解释。
- 小分红（10派1，缺口约-0.5%）靠 TDX 缺口预筛会漏，必须靠本接口的日期判断，不能只看缺口。
- THS 仍保留为 QFQ 数据源，其限流靠“少调”解决，不靠“调优 UA/并发”解决。

## 8. 待确认（股票部分，已决）

- ~~首版单股查询够用，还是必须首版就支持批量？~~ → 已实现单股+批量（`?code=600519,000001`，上限50只）。
- 缓存/预热暂缓（无缓存版已上线实测）。
- 口径：金额为每10股，float32 尾差调用方保留2位小数。

## 9. ETF 部分（1500只，方案评审中，未实现）

### 9.1 现状验证（2026-09-11 实测）

- `0x000f` 对ETF无记录：`588290`（按 `AddPrefix` 规则判 `sh`）实测 `num=0`。预期内：ETF分红不在股票股本变迁口径，走本接口返回 `{"records":[]}`，不进 `not_found`。
- THS 对ETF有复权序列：`510300` 的 `qfq（01）` 一次拿到 3475 天（2012年上市以来全量）；但 `bfq（00）` 与 `hfq（02）` 连续 `502`，且短时间内第4次请求即被限——再次证明**不可高频轮询 THS**，THS 只配做嫌疑股对账。
- 代码前缀缺口：`protocol.AddPrefix` 只认 `510/511/512/513/515/159`，`588/56/55` 等ETF号段调 K 线类接口会报“股票代码长度错误”（`DecodeCode` 要求8位）；`IsETF` 认 `51/56/58/15/16`，两处不一致。拟改成 `5开头→sh、1开头→sz` 兜底（6位里5/1开头的不是股票，安全），两市撞号的可转债传全称（如 `sh113XXX`）。

### 9.2 方案：排期表（每天查一次，白天跑）

> 验证结论（2026-09-11）：akshare 的基金分红明细走的是东财 `fundf10/.../fhsp_{code}.html` **单只页面**——1500只天天扫必被封，此路出局；新浪ETF分红未定位到可用批量口，暂不承诺。THS 短时4连调即 `502`，同样只能做嫌疑股对账。

1. **排期来源：投资数据网为主（已验证通过），巨潮备选**：
   - 投资数据网基金公告口：`POST /fund/ajax/announcement/`，体 `{category:"5",follow:"0",search:"",offset(1-based页码，0会500),pagesize}`，需完整登录Cookie + `X-Requested-With/Referer` 头；`category`：0全部/1招募设立/2财务报表/5分红/4拆分折算/3其它。返回 `rsm.data[]`：`fundCode`（`jj`开头场外/`sh`开头场内ETF）、`fundShortName`、`reportName`、`reportSendDate`、`adjunctUrl`（证监会eid站PDF）。分红类累计 `total=1000` 可翻页，ETF/联接/REIT/债基全覆盖。
   - 只有标题流：除息日/每份金额必须下正文PDF抓，每天命中的才下（几十篇）。每天白天扫增量（按 `reportSendDate` 过滤昨天/今天，翻1-3页），低频无封号之忧；Cookie会过期（**实测约1-2小时即失效，`login_check errno=-1`**），服务端已做探活+失败明示（`Cookie失效，需重抓`），生产需每日重抓或找站方要长效token；Cookie放环境变量 `TOUZID_COOKIE`，**禁止进仓库**。
   - 股票侧：本站为fund口，股票分红实施公告需另确认站内company口（待验证），否则走巨潮标题流（见下）。
   - 巨潮备选（已验证一半）：`hisAnnouncement/query` 时间倒序翻页正常、无需登录、连调约20次无封锁；但服务端过滤实测全部失效，只能时间流全扫+本地标题过滤（每天 `<30` 次）；`adjunctUrl` 为 `finalpage/YYYY-MM-DD/<id>.PDF`（直链200可下，同名TXT 404）；**正文三要素提取待分红季拿真实实施公告按PDF解析联调**。
   - 三组关键词全扫（只认实施）：
   - `利润分配 + 实施` → 现金分红+送股+转增（占99%以上，都在同一份实施公告里）
   - `配股`（认 `配股提示性公告`/`配股说明书`）→ 配股除权，除权规则同分红（登记日次一交易日）；一年几单但缺口大，不可漏
   - `收益分配/分红公告` → ETF和基金分红
   - 拆/合股：A股现代市场基本绝迹（即 `0x000f` 11/12类股改缩股），真有走重大事项公告，遇到了按 `F` 套公式，不单独建流
   - **实测结论（2026-09-11）**：`hisAnnouncement/query` 时间倒序翻页正常、无需登录、连调约20次无封锁；但服务端 `searchkey/stock/seDate` 过滤实测全部失效（无指纹会话时被忽略），故只能时间流全扫+本地标题过滤——淡季每天几页、旺季十几页，每天请求 `<30` 次，满足不被封约束；`adjunctUrl` 格式为 `finalpage/YYYY-MM-DD/<id>.PDF`（PDF直链200可下，TXT同名 pattern 404）；**正文三要素（登记日/除息日/方案）提取待分红季（5-7月）拿真实实施公告联调**，实现侧按 PDF 解析预留。
2. **日常（零日常调用）**：1500只ETF日BFQ走TDX连接池（同股票通道，不封IP）；QFQ本地现算，步长 `(P_prev-D)/P_prev`（`D`单位元/份=元/股，复用股票公式 `S=0`）；只在排期表中的除息日当天处理该ETF。
3. **第三方节奏不变**：白天查排期表 → 22点后按表拉THS全量（仅命中股）；`xdxr`保留做股票侧兜底校验。
4. **代码改动**：`AddPrefix` 补 ETF 兜底（见9.1）；新增排期抓取定时任务+日历表+查询接口（暂未实现）。
5. Sina 1500只串行月查降级为备选（待验证批量口）。

### 9.3 设计原则（已定）：数据源抽象，多源兼容

- 排期表统一结构：`code / 类型(股票/ETF) / ex_date / record_date / 方案 / 进度(预案·实施) / source(giant/touzid/tushare/em) / updated_at`，按 `(code, ex_date, source)` 去重，多源可交叉验证。
- 抓取器按源独立实现同一接口（增量拉取→正文解析→入库），首批只实现**巨潮**，投资数据网（已验证口径，作为第二个源预留位，Cookie探活+secret管理一并预留），Tushare/东财以后按需加。
- 查询接口不暴露来源，第三方只按日期/代码查，内部多源合并。
- 定时配置（环境变量，`web/excalendar.go`）：`EXCAL_LIGHT_INTERVAL_HOURS`（巨潮循环，默认6）、`EXCAL_HEAVY_INTERVAL_HOURS`（xdxr全市场，默认24）；touzid默认屏蔽，需 `EXCAL_TOUZID=1` + `TOUZID_COOKIE` 才启用。

### 9.4 待确认（ETF部分）

- 有无 Tushare token？有走 A，无走 B。
- `AddPrefix` 的 `5→sh、1→sz` 兜底是否接受？
- 1500只的 ETF 清单及主要号段（确认前缀覆盖无遗漏）。

## 10. 实现落点（已上线实测，2026-09-11，未推送）

### 文件

| 文件 | 内容 |
|---|---|
| `protocol/const.go` | 启用 `TypeXdxr=0x000f` |
| `protocol/model_xdxr.go`（新建） | 新协议头组包+29字节记录解码（`category==1` 取分红/配股价/送转/配股，每10股口径） |
| `protocol/model_connect.go` | 注册 `MXdxr` |
| `client.go` | `handlerDealMessage` 加 `TypeXdxr` 分支；`GetXdxrInfo(code)`（独立MsgID序列 `0x10000000+`，服务端回显匹配，不走 `SendFrame`） |
| `web/xdxr.go`（新建） | `GET /api/xdxr?code=` 单股+批量（上限50只），`{count,list,not_found}` |
| `web/excalendar.go`（新建） | 排期表+三抓取器+定时+查询接口（见下） |
| `web/server.go` | 注册 `/api/xdxr`、`/api/ex-calendar`、`/api/ex-calendar/refresh`；启动调 `InitExCalendar()` |
| `docker-compose.yml` | 加排期环境变量+中文注解；`./data` 须为tfg属主（root属主会导致容器内写库失败重启） |

### 排期表（`data/database/excalendar.db`）

- `ex_calendar`：`id` 自增主键 + `UNIQUE(code,ex_date,source,title)`；字段 `code/market/type/ex_date/record_date/pay_date/announce_date/div_cash/songzhuan/peigu/peigujia/div_proc/source/title/raw/updated_at`；索引 `ex_date`、`(code,ex_date)`。
- `ex_fetch_state`：`source/last_ok/last_error/cursor`，各源抓取状态。
- 同一事件多源各存一行（`source` 参与唯一键，互不覆盖）；按天查只命中 `ex_date` 非空行，按股查以 `source=xdxr` 的金额行为准。

### 抓取器与定时（`web/excalendar.go`，首次启动后2/5分钟各跑一次）

- `xdxr`：全市场股票，连接池4并发扫 `GetXdxrInfo`，`category==1` 入库（`div_proc=实施`），约10~20分钟/轮。
- `giant`：巨潮标题流翻页到跨天（上限40页，页间1秒），三组关键词本地过滤入库（标题级，`ex_date` 为空待正文解析回填）。
- `touzid`：默认屏蔽（需 `EXCAL_TOUZID=1` + `TOUZID_COOKIE`）；Cookie实测约1-2小时失效，探活用 `login_check`，失败明示重抓。
- 环境变量：`EXCAL_LIGHT_INTERVAL_HOURS`（默认6）、`EXCAL_HEAVY_INTERVAL_HOURS`（默认24）。

### 接口

```text
GET  /api/xdxr?code=600519 / ?code=600519,000001
GET  /api/ex-calendar?date=YYYYMMDD & code= & type=stock/etf & source=
POST /api/ex-calendar/refresh?source=xdxr&codes=..(≤50) / source=giant / source=touzid
```

### 实测证据（docker实测容器，非compose生产容器）

- `/api/xdxr?code=600519` → 45条，与公开分红全对上（2024-06-19/308.76等）；批量 `600519,000001` → 45+80条；错码进 `not_found`。
- `refresh?source=xdxr&codes=600519,000001` → `ok=2`；`?code=600519` → 30条cat1；`?date=20240619` → 命中。
- `refresh?source=giant` → `matched=0`（当日无实施公告，符合预期）；无Cookie调touzid → 优雅跳过；有过期Cookie → 明示`Cookie失效，需重抓`。
- compose生产容器已用同样镜像验证：`health/xdxr/refresh/ex-calendar` 全通。

### 未做（留待后续）

- 正文三要素PDF解析回填（待分红季联调）。
- `AddPrefix` ETF号段兜底（待确认）。
- touzid启用（需长效Cookie/token）与站内company口验证。
- 缓存/预热（已暂缓）。

## 11. 第三方使用指南

### 时刻1：白天收盘后（16点左右，1次）——先查今天谁除权

```python
r = GET /api/ex-calendar?date=20260911
# data.list 里有谁，今晚就重点处理谁；为空今天无事
```

### 时刻2：对名单里的（22点后，逐只）——拉THS全量覆盖

```python
for code in 名单:
    full = GET /api/kline-all/ths?code={code}&type=day  # 全量删了重插
# 不在名单的走老增量，不用动
```

### 时刻3：单只核对（按需，不用天天轮）

```text
GET /api/xdxr?code=600519            # 单只全历史，看有没有date==今天的category==1
GET /api/xdxr?code=600519,000001     # 批量，最多50只
```

### 运维补数（别写进日常任务）

```text
POST /api/ex-calendar/refresh?source=xdxr&codes=600519   # 漏扫手动补，≤50只
POST /api/ex-calendar/refresh?source=giant               # 白天标题流手动补一次
```

### 红线

1. `ex-calendar` 一天查1-2次就够，数据一天一变，别高频轮询。
2. `xdxr` 返回全历史（40-80条），只看 `date==今天` 那条，别拿全量当增量。
3. `refresh` 是运维口，别拿它扫全市场（全量走夜里定时任务）。
4. `ex-calendar` 按股查会混着 `giant` 标题行（`ex_date` 为空），认准 `source=xdxr` 的有金额行；按天查只命中日期确定的行，直接用。
