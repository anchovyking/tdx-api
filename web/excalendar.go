package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/injoyai/tdx"
	"github.com/injoyai/tdx/protocol"
)

// 除权除息排期表(多源兼容: xdxr/巨潮/投资数据网/...)
// 设计见 docs/除权除息方案.md 第9节

const (
	excalDBPath = "data/database/excalendar.db"
)

var (
	excalDB   *sql.DB
	excalMu   sync.Mutex
	excalOnce sync.Once
)

// InitExCalendar 初始化排期库并启动定时抓取
func InitExCalendar() {
	excalOnce.Do(func() {
		if err := os.MkdirAll("data/database", 0755); err != nil {
			log.Printf("创建排期数据目录失败: %v", err)
		}
		db, err := sql.Open("sqlite", excalDBPath)
		if err != nil {
			log.Printf("打开排期数据库失败: %v", err)
			return
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ex_calendar (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT DEFAULT '',
			market TEXT DEFAULT '',
			type TEXT DEFAULT '',
			ex_date TEXT DEFAULT '',
			record_date TEXT DEFAULT '',
			pay_date TEXT DEFAULT '',
			announce_date TEXT DEFAULT '',
			div_cash REAL DEFAULT 0,
			songzhuan REAL DEFAULT 0,
			peigu REAL DEFAULT 0,
			peigujia REAL DEFAULT 0,
			div_proc TEXT DEFAULT '',
			source TEXT DEFAULT '',
			title TEXT DEFAULT '',
			raw TEXT DEFAULT '',
			updated_at TIMESTAMP,
			UNIQUE(code, ex_date, source, title)
		); CREATE INDEX IF NOT EXISTS idx_excal_date ON ex_calendar(ex_date);
		CREATE INDEX IF NOT EXISTS idx_excal_code ON ex_calendar(code, ex_date);
		CREATE TABLE IF NOT EXISTS ex_fetch_state (
			source TEXT PRIMARY KEY,
			last_ok TIMESTAMP,
			last_error TEXT DEFAULT '',
			cursor TEXT DEFAULT ''
		)`); err != nil {
			log.Printf("初始化排期表失败: %v", err)
			return
		}
		excalDB = db
		go excalLightLoop() //巨潮+touzid,轻量高频
		go excalHeavyLoop() //xdxr全市场,重量低频
	})
}

// excalRow 排期行
type excalRow struct {
	Code         string  `json:"code"`
	Market       string  `json:"market"`
	Type         string  `json:"type"`
	ExDate       string  `json:"ex_date"`
	RecordDate   string  `json:"record_date"`
	PayDate      string  `json:"pay_date"`
	AnnounceDate string  `json:"announce_date"`
	DivCash      float64 `json:"div_cash"`
	Songzhuan    float64 `json:"songzhuan"`
	Peigu        float64 `json:"peigu"`
	Peigujia     float64 `json:"peigujia"`
	DivProc      string  `json:"div_proc"`
	Source       string  `json:"source"`
	Title        string  `json:"title"`
	UpdatedAt    string  `json:"updated_at"`
}

func excalUpsert(r excalRow) {
	if excalDB == nil {
		return
	}
	excalMu.Lock()
	defer excalMu.Unlock()
	_, err := excalDB.Exec(`INSERT INTO ex_calendar
		(code,market,type,ex_date,record_date,pay_date,announce_date,div_cash,songzhuan,peigu,peigujia,div_proc,source,title,raw,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(code,ex_date,source,title) DO UPDATE SET
		market=excluded.market,type=excluded.type,record_date=excluded.record_date,pay_date=excluded.pay_date,
		announce_date=excluded.announce_date,div_cash=excluded.div_cash,songzhuan=excluded.songzhuan,
		peigu=excluded.peigu,peigujia=excluded.peigujia,div_proc=excluded.div_proc,raw=excluded.raw,updated_at=excluded.updated_at`,
		r.Code, r.Market, r.Type, r.ExDate, r.RecordDate, r.PayDate, r.AnnounceDate,
		r.DivCash, r.Songzhuan, r.Peigu, r.Peigujia, r.DivProc, r.Source, r.Title, "",
		time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		log.Printf("排期入库失败 %s/%s: %v", r.Source, r.Code, err)
	}
}

func excalMarkState(source, cursor, errStr string) {
	if excalDB == nil {
		return
	}
	excalMu.Lock()
	defer excalMu.Unlock()
	now := time.Now().Format("2006-01-02 15:04:05")
	if errStr == "" {
		_, _ = excalDB.Exec(`INSERT INTO ex_fetch_state(source,last_ok,last_error,cursor) VALUES(?,?,?,?)
			ON CONFLICT(source) DO UPDATE SET last_ok=excluded.last_ok,last_error='',cursor=excluded.cursor`,
			source, now, cursor)
	} else {
		_, _ = excalDB.Exec(`INSERT INTO ex_fetch_state(source,last_ok,last_error,cursor) VALUES(?,?,?,?)
			ON CONFLICT(source) DO UPDATE SET last_error=excluded.last_error,cursor=excluded.cursor`,
			source, now, errStr, cursor)
	}
}

// excalMarket 由6位代码推市场,推不出返回空
func excalMarket(code6 string) string {
	if len(code6) != 6 {
		return ""
	}
	full := protocol.AddPrefix(code6)
	ex, _, err := protocol.DecodeCode(full)
	if err != nil {
		return ""
	}
	return ex.String()
}

// ============ 抓取器1: xdxr全量(股票,日期+金额完整) ============

func excalFetchXdxr(codes []string) (ok, fail int) {
	if manager == nil {
		return 0, len(codes)
	}
	start := time.Now()
	lastLog := start
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	done := 0
	failCodes := make([]string, 0, 50) //失败样本,最多记录50个
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c string) {
			defer wg.Done()
			defer func() { <-sem }()
			//规范化带前缀,用于品种判定与短码提取
			full := protocol.AddPrefix(c)
			isEtf := protocol.IsETF(full)
			var resp *protocol.XdxrResp
			err := manager.Do(func(cli *tdx.Client) error {
				var e error
				resp, e = cli.GetXdxrInfo(c)
				return e
			})
			mu.Lock()
			defer mu.Unlock()
			done++
			//进度日志:每200只或每10秒打印一次
			if done%200 == 0 || time.Since(lastLog) >= 10*time.Second {
				lastLog = time.Now()
				log.Printf("xdxr排期进度 %d/%d 成功%d 失败%d 已耗时%v",
					done, len(codes), ok, fail, time.Since(start).Round(time.Second))
			}
			if err != nil || resp == nil {
				fail++
				if len(failCodes) < 50 {
					if err != nil {
						failCodes = append(failCodes, fmt.Sprintf("%s(%v)", c, err))
					} else {
						failCodes = append(failCodes, fmt.Sprintf("%s(空响应)", c))
					}
				}
				return
			}
			short := full
			if len(full) == 8 {
				short = full[2:]
			}
			typ := "stock"
			if isEtf {
				typ = "etf"
			}
			for _, r := range resp.Records {
				if r.Category != 1 {
					continue
				}
				excalUpsert(excalRow{
					Code: short, Market: excalMarket(short), Type: typ,
					ExDate: r.DateStr, DivCash: r.Fenhong, Songzhuan: r.Songzhuangu,
					Peigu: r.Peigu, Peigujia: r.Peigujia, DivProc: "实施", Source: "xdxr",
					Title: r.CategoryName,
				})
			}
			ok++
		}(code)
	}
	wg.Wait()
	excalMarkState("xdxr", fmt.Sprintf("ok=%d fail=%d", ok, fail), "")
	log.Printf("xdxr排期抓取完成 共%d只 成功%d 失败%d 耗时%v",
		len(codes), ok, fail, time.Since(start).Round(time.Second))
	if len(failCodes) > 0 {
		log.Printf("xdxr失败样本(%d个): %s", len(failCodes), strings.Join(failCodes, ", "))
	}
	return ok, fail
}

// ============ 抓取器2: 巨潮标题流(标题级,正文解析TODO) ============

var excalGiantKW = []string{"分红", "派息", "利润分配", "送股", "转增", "配股", "收益分配"}

func excalGiantMatch(title string) bool {
	if strings.Contains(title, "实施") && containsAny(title, "分红", "派息", "利润分配", "送股", "转增") {
		return true
	}
	if strings.Contains(title, "配股") {
		return true
	}
	if strings.Contains(title, "收益分配") {
		return true
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

type giantAnn struct {
	SecCode           string `json:"secCode"`
	SecName           string `json:"secName"`
	AnnouncementTitle string `json:"announcementTitle"`
	AnnouncementTime  int64  `json:"announcementTime"`
	AdjunctUrl        string `json:"adjunctUrl"`
}

func excalFetchGiant() (matched int, err error) {
	today := time.Now().Format("2006-01-02")
	cli := &http.Client{Timeout: 20 * time.Second}
	for page := 1; page <= 40; page++ {
		body, _ := json.Marshal(map[string]interface{}{
			"pageNum": page, "pageSize": 30, "column": "szse", "tabName": "fulltext",
			"plate": "sz;sh;bj", "stock": "", "searchkey": "", "secid": "",
			"category": "", "trade": "", "seDate": "",
		})
		req, _ := http.NewRequest("POST", "http://www.cninfo.com.cn/new/hisAnnouncement/query",
			bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := cli.Do(req)
		if err != nil {
			excalMarkState("giant", fmt.Sprintf("page=%d", page), err.Error())
			return matched, err
		}
		bs, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var out struct {
			Announcements []giantAnn `json:"announcements"`
		}
		if err := json.Unmarshal(bs, &out); err != nil || len(out.Announcements) == 0 {
			break
		}
		oldest := ""
		for _, a := range out.Announcements {
			d := time.UnixMilli(a.AnnouncementTime).Format("2006-01-02")
			oldest = d
			if len(a.SecCode) != 6 {
				continue //港股等非6位代码跳过
			}
			if !excalGiantMatch(a.AnnouncementTitle) {
				continue
			}
			proc := "预案"
			if strings.Contains(a.AnnouncementTitle, "实施") {
				proc = "实施"
			}
			excalUpsert(excalRow{
				Code: a.SecCode, Market: excalMarket(a.SecCode), Type: "stock",
				AnnounceDate: d, DivProc: proc, Source: "giant", Title: a.AnnouncementTitle,
			})
			matched++
		}
		if oldest < today {
			break
		}
		time.Sleep(time.Second)
	}
	excalMarkState("giant", fmt.Sprintf("matched=%d", matched), "")
	return matched, nil
}

// ============ 抓取器3: 投资数据网基金分红(标题级,需Cookie) ============

func excalFetchTouzid() (matched int, err error) {
	cookie := strings.TrimSpace(os.Getenv("TOUZID_COOKIE"))
	if cookie == "" {
		msg := "TOUZID_COOKIE 未配置,跳过"
		excalMarkState("touzid", "", msg)
		return 0, fmt.Errorf("%s", msg)
	}
	today := time.Now().Format("2006-01-02")
	cli := &http.Client{Timeout: 20 * time.Second}
	//探活
	req, _ := http.NewRequest("GET", "https://www.touzid.com/account/ajax/login_check/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Cookie", cookie)
	resp, err := cli.Do(req)
	if err != nil {
		excalMarkState("touzid", "", "探活失败:"+err.Error())
		return 0, err
	}
	bs, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var probe struct {
		Errno int `json:"errno"`
	}
	if json.Unmarshal(bs, &probe) != nil || probe.Errno != 1 {
		msg := fmt.Sprintf("Cookie失效(errno=%d),需重抓", probe.Errno)
		excalMarkState("touzid", "", msg)
		return 0, fmt.Errorf("%s", msg)
	}
	for page := 1; page <= 40; page++ {
		payload, _ := json.Marshal(map[string]interface{}{
			"category": "5", "follow": "0", "search": "", "offset": page, "pagesize": 25,
		})
		req, _ := http.NewRequest("POST", "https://www.touzid.com/fund/ajax/announcement/",
			bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0")
		req.Header.Set("Referer", "https://www.touzid.com/fund/report.html")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Cookie", cookie)
		resp, err := cli.Do(req)
		if err != nil {
			excalMarkState("touzid", fmt.Sprintf("page=%d", page), err.Error())
			return matched, err
		}
		bs, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var out struct {
			Rsm struct {
				Data []struct {
					FundCode       string `json:"fundCode"`
					FundShortName  string `json:"fundShortName"`
					ReportName     string `json:"reportName"`
					ReportSendDate string `json:"reportSendDate"`
				} `json:"data"`
			} `json:"rsm"`
		}
		if json.Unmarshal(bs, &out) != nil || len(out.Rsm.Data) == 0 {
			break
		}
		oldest := ""
		for _, d := range out.Rsm.Data {
			oldest = d.ReportSendDate
			code, market, typ := d.FundCode, "", "fund"
			if strings.HasPrefix(code, "jj") {
				typ = "fund"
				if strings.Contains(d.FundShortName, "REIT") {
					typ = "reit"
				}
			} else if len(code) == 8 {
				market = code[:2]
				code = code[2:]
				typ = "etf"
			} else {
				continue
			}
			excalUpsert(excalRow{
				Code: code, Market: market, Type: typ,
				AnnounceDate: d.ReportSendDate, DivProc: "实施", Source: "touzid", Title: d.ReportName,
			})
			matched++
		}
		if oldest < today {
			break
		}
		time.Sleep(time.Second)
	}
	excalMarkState("touzid", fmt.Sprintf("matched=%d", matched), "")
	return matched, nil
}

// ============ 定时 ============
//
// 时间配置(环境变量,容器里加 -e 即可,不配则用默认值):
//   EXCAL_LIGHT_INTERVAL_HOURS  轻量循环间隔小时,默认6
//   EXCAL_HEAVY_INTERVAL_HOURS  重量循环(xdxr全市场)间隔小时,默认24
//   EXCAL_GIANT                 设为1才启用巨潮抓取(默认停用,除权数据已由xdxr全覆盖)
//   EXCAL_TOUZID                设为1才启用投资数据网抓取(默认屏蔽,需同时配 TOUZID_COOKIE)

func excalEnvHours(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func excalLightLoop() {
	time.Sleep(2 * time.Minute)
	interval := excalEnvHours("EXCAL_LIGHT_INTERVAL_HOURS", 6)
	for {
		//巨潮默认停用(除权数据已由0x000f全覆盖),需 EXCAL_GIANT=1 才启用
		if os.Getenv("EXCAL_GIANT") == "1" {
			if _, err := excalFetchGiant(); err != nil {
				log.Printf("巨潮排期抓取失败: %v", err)
			}
		}
		if os.Getenv("EXCAL_TOUZID") == "1" {
			if _, err := excalFetchTouzid(); err != nil {
				log.Printf("投资数据网排期抓取: %v", err)
			}
		} //默认屏蔽,需 EXCAL_TOUZID=1 + TOUZID_COOKIE 才启用
		time.Sleep(time.Duration(interval) * time.Hour)
	}
}

func excalHeavyLoop() {
	time.Sleep(5 * time.Minute)
	interval := excalEnvHours("EXCAL_HEAVY_INTERVAL_HOURS", 24)
	for {
		if manager != nil {
			stocks := manager.Codes.GetStocks()
			etfs := manager.Codes.GetETFs()
			codes := make([]string, 0, len(stocks)+len(etfs))
			codes = append(codes, stocks...)
			codes = append(codes, etfs...)
			log.Printf("xdxr排期全量开始 股票%d只+ETF%d只 共%d只", len(stocks), len(etfs), len(codes))
			ok, fail := excalFetchXdxr(codes)
			log.Printf("xdxr排期全量完成 ok=%d fail=%d (股票%d+ETF%d)", ok, fail, len(stocks), len(etfs))
		}
		time.Sleep(time.Duration(interval) * time.Hour)
	}
}

// ============ 查询接口 ============

// handleGetExCalendar 查询除权除息排期
// 示例: GET /api/ex-calendar                       (不传date/code时默认查今天)
//      GET /api/ex-calendar?date=20260619
//      GET /api/ex-calendar?code=600519
//      GET /api/ex-calendar?date=20260619&type=etf
func handleGetExCalendar(w http.ResponseWriter, r *http.Request) {
	if excalDB == nil {
		errorResponse(w, "排期模块未初始化")
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	date = strings.ReplaceAll(date, "-", "")
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if len(code) == 8 {
		code = code[2:]
	}
	//不传date也不传code时,默认查当天
	if date == "" && code == "" {
		date = time.Now().Format("20060102")
	}
	typ := strings.TrimSpace(r.URL.Query().Get("type"))
	source := strings.TrimSpace(r.URL.Query().Get("source"))

	cond := []string{"1=1"}
	args := []interface{}{}
	if date != "" {
		if len(date) != 8 {
			errorResponse(w, "date 格式错误，应为 YYYYMMDD")
			return
		}
		cond = append(cond, "ex_date=?")
		args = append(args, date[:4]+"-"+date[4:6]+"-"+date[6:])
	}
	if code != "" {
		cond = append(cond, "code=?")
		args = append(args, code)
	}
	if typ != "" {
		cond = append(cond, "type=?")
		args = append(args, typ)
	}
	if source != "" {
		cond = append(cond, "source=?")
		args = append(args, source)
	}
	rows, err := excalDB.Query(`SELECT code,market,type,ex_date,record_date,pay_date,announce_date,
		div_cash,songzhuan,peigu,peigujia,div_proc,source,title,
		COALESCE(updated_at,'') FROM ex_calendar WHERE `+strings.Join(cond, " AND ")+
		` ORDER BY ex_date DESC, code LIMIT 2000`, args...)
	if err != nil {
		errorResponse(w, "查询排期失败: "+err.Error())
		return
	}
	defer rows.Close()
	list := make([]excalRow, 0)
	for rows.Next() {
		var x excalRow
		if err := rows.Scan(&x.Code, &x.Market, &x.Type, &x.ExDate, &x.RecordDate, &x.PayDate,
			&x.AnnounceDate, &x.DivCash, &x.Songzhuan, &x.Peigu, &x.Peigujia,
			&x.DivProc, &x.Source, &x.Title, &x.UpdatedAt); err != nil {
			continue
		}
		list = append(list, x)
	}
	successResponse(w, map[string]interface{}{
		"count": len(list),
		"list":  list,
	})
}

// handleRefreshExCalendar 手动触发排期刷新(运维用)
// POST /api/ex-calendar/refresh?source=xdxr&codes=600519,000001
// POST /api/ex-calendar/refresh?source=giant
// POST /api/ex-calendar/refresh?source=touzid
func handleRefreshExCalendar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, "只支持POST请求")
		return
	}
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	switch source {
	case "xdxr":
		codes := splitCodes(r.URL.Query().Get("codes"))
		if len(codes) == 0 {
			errorResponse(w, "codes 不能为空")
			return
		}
		if len(codes) > 50 {
			errorResponse(w, "一次最多刷新50只")
			return
		}
		ok, fail := excalFetchXdxr(codes)
		successResponse(w, map[string]interface{}{"ok": ok, "fail": fail})
	case "giant":
		matched, err := excalFetchGiant()
		if err != nil {
			errorResponse(w, fmt.Sprintf("巨潮抓取失败: %v", err))
			return
		}
		successResponse(w, map[string]interface{}{"matched": matched})
	case "touzid":
		matched, err := excalFetchTouzid()
		if err != nil {
			errorResponse(w, fmt.Sprintf("投资数据网抓取失败: %v", err))
			return
		}
		successResponse(w, map[string]interface{}{"matched": matched})
	default:
		errorResponse(w, "source 仅支持 xdxr/giant/touzid")
	}
}
