package main

import (
	"database/sql"
	"fmt"
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

// 除权除息排期表(来源 xdxr，见 docs/除权除息方案.md)

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
		db, err := sql.Open("sqlite", tdx.SqliteDSN(excalDBPath))
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
		schedRegister("excal", func(trigger, arg string) (string, error) {
			return excalRunOnce(trigger, arg)
		})
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

// ============ 定时(xdxr全市场，由统一调度按 config.yaml#tasks.excal 触发) ============

// excalRunOnce 单次执行：arg 为空跑全市场（股票+ETF），非空则只跑指定代码（逗号分隔，≤50）
func excalRunOnce(trigger, arg string) (string, error) {
	var codes []string
	if strings.TrimSpace(arg) != "" {
		codes = splitCodes(arg)
		if len(codes) > 50 {
			return "", fmt.Errorf("一次最多刷新50只")
		}
		if len(codes) == 0 {
			return "", fmt.Errorf("codes 为空")
		}
	} else {
		if manager == nil {
			return "", fmt.Errorf("数据管理器未初始化")
		}
		stocks := manager.Codes.GetStocks()
		etfs := manager.Codes.GetETFs()
		codes = make([]string, 0, len(stocks)+len(etfs))
		codes = append(codes, stocks...)
		codes = append(codes, etfs...)
		log.Printf("xdxr排期全量开始 股票%d只+ETF%d只 共%d只", len(stocks), len(etfs), len(codes))
	}
	start := time.Now()
	ok, fail := excalFetchXdxr(codes)
	detail := fmt.Sprintf("xdxr排期完成：成功%d，失败%d，共%d只，耗时%s",
		ok, fail, len(codes), tdx.FormatDuration(time.Since(start)))
	log.Print(detail)
	if fail > 0 && ok == 0 {
		return detail, fmt.Errorf("全部失败 fail=%d", fail)
	}
	return detail, nil
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
