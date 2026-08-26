package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/injoyai/tdx/protocol"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	industryDBPath      = "data/database/industry.db"
	industryHTTPTimeout = 10 * time.Second
)

var (
	industryDB   *sql.DB
	industryMu   sync.Mutex
	industryOnce sync.Once
)

// InitIndustry 初始化行业数据存储
func InitIndustry() {
	industryOnce.Do(func() {
		db, err := sql.Open("sqlite", industryDBPath)
		if err != nil {
			log.Printf("打开行业数据库失败: %v", err)
			return
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS industry (
			code      TEXT PRIMARY KEY,
			name      TEXT,
			industry  TEXT,
			industry1 TEXT DEFAULT '',
			industry2 TEXT DEFAULT '',
			source    TEXT DEFAULT '',
			updated_at TIMESTAMP
		); CREATE TABLE IF NOT EXISTS industry_failed (
			code      TEXT PRIMARY KEY,
			fails     INTEGER DEFAULT 0,
			last_fail TIMESTAMP
		)`); err != nil {
			log.Printf("初始化行业表失败: %v", err)
			return
		}
		for _, col := range []string{"industry1", "industry2", "source"} {
			db.Exec("ALTER TABLE industry ADD COLUMN " + col + " TEXT DEFAULT ''")
		}
		industryDB = db
		go industryWarmLoop()
	})
}

// warmMu 预热互斥锁：行业与ETF预热串行执行，避免并发触发限流
var warmMu sync.Mutex

// industryWarmLoop 启动后自动补齐缺失行业数据，之后每天检查一次新增股票
func industryWarmLoop() {
	time.Sleep(1 * time.Minute)
	for {
		warmMu.Lock()
		missing, err := industryMissingCodes()
		if err != nil {
			log.Printf("行业预热检查失败: %v", err)
		} else if len(missing) == 0 {
			log.Printf("行业数据已全部缓存")
		} else {
			log.Printf("行业预热开始，待补 %d 只", len(missing))
			ok := 0
			failCount := 0
			consecutiveFails := 0
			start := time.Now()
			for i, code := range missing {
				_, fetchErr := industryFetchSingle(code)
				if fetchErr != nil {
					failCount++
					log.Printf("行业预热[%d/%d] %s 失败: %v", i+1, len(missing), code, fetchErr)
					consecutiveFails++
					if consecutiveFails >= 5 {
						log.Printf("行业预热连续失败%d次，暂停5分钟防限流", consecutiveFails)
						time.Sleep(5 * time.Minute)
						consecutiveFails = 0
					}
				} else {
					ok++
					consecutiveFails = 0
				}
				industryMarkResult(code, fetchErr == nil)
				if (i+1)%5 == 0 {
					log.Printf("行业预热进度 %d/%d 成功%d 失败%d 已耗时%.0f分钟",
						i+1, len(missing), ok, failCount, time.Since(start).Minutes())
				}
				time.Sleep(3 * time.Second)
			}
			log.Printf("行业预热完成，成功 %d/%d，耗时 %.0f 分钟", ok, len(missing), time.Since(start).Minutes())
		}
		warmMu.Unlock()
		time.Sleep(24 * time.Hour)
	}
}

// industryMissingCodes 全市场代码减去已缓存代码
func industryMissingCodes() ([]string, error) {
	allCodes, err := getAllCodeModels()
	if err != nil {
		return nil, err
	}
	cached := make(map[string]bool)
	rows, err := industryDB.Query("SELECT code FROM industry")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code string
		if rows.Scan(&code) == nil {
			cached[code] = true
		}
	}
	rows.Close()

	missing := make([]string, 0)
	now := time.Now()
	for _, model := range allCodes {
		if !protocol.IsStock(model.FullCode()) {
			continue
		}
		if !cached[model.Code] && !industrySkipFailed(model.Code, now) {
			missing = append(missing, model.Code)
		}
	}
	return missing, nil
}

// industrySkipFailed 失败≥3次且30天内不再重试
func industrySkipFailed(code string, now time.Time) bool {
	var fails int
	var lastFail string
	if err := industryDB.QueryRow(
		"SELECT fails,last_fail FROM industry_failed WHERE code=?", code).
		Scan(&fails, &lastFail); err != nil {
		return false
	}
	if fails < 3 {
		return false
	}
	t, _ := time.Parse(time.RFC3339, lastFail)
	return now.Sub(t) < 30*24*time.Hour
}

// industryMarkResult 记录成功（清除失败记录）或失败（累加次数）
func industryMarkResult(code string, success bool) {
	if success {
		industryDB.Exec("DELETE FROM industry_failed WHERE code=?", code)
		return
	}
	industryDB.Exec(`INSERT INTO industry_failed(code,fails,last_fail) VALUES(?,1,?)
		ON CONFLICT(code) DO UPDATE SET fails=fails+1, last_fail=excluded.last_fail`,
		code, time.Now().Format(time.RFC3339))
}

var industryHTTPClient = &http.Client{
	Timeout: industryHTTPTimeout,
	Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, "tcp4", addr)
		},
	},
}

type industryInfo struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Industry  string `json:"-"`
	Industry1 string `json:"industry1"`
	Industry2 string `json:"industry2"`
	Source    string `json:"source"`
}

// industryFetchTHS 同花顺F10获取申万一级-二级行业（缓存miss时实时抓取）
func industryFetchTHS(code string) (*industryInfo, error) {
	url := fmt.Sprintf("https://basic.10jqka.com.cn/%s/company.html", code)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Referer", "https://basic.10jqka.com.cn")
	resp, err := industryHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ths http状态码: %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`行业：</strong><span>\s*([^<]+?)\s*</span>`)
	m := re.FindSubmatch(decoded)
	if m == nil {
		return nil, fmt.Errorf("ths未解析到行业数据")
	}
	info := &industryInfo{Code: code, Source: "ths"}
	titleRe := regexp.MustCompile(`<title>\s*([^()<>]+?)\s*\(`)
	if tm := titleRe.FindSubmatch(decoded); tm != nil {
		info.Name = strings.TrimSpace(string(tm[1]))
	}
	text := strings.TrimSpace(string(m[1]))
	parts := strings.SplitN(text, "—", 2)
	info.Industry1 = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		info.Industry2 = strings.TrimSpace(parts[1])
	}
	info.Industry = info.Industry2
	if info.Industry == "" {
		info.Industry = info.Industry1
	}
	return info, nil
}

// industryFetchSingle 单只查询（缓存miss时调用）
func industryFetchSingle(code string) (*industryInfo, error) {
	info, err := industryFetchTHS(code)
	if err != nil {
		return nil, err
	}
	industrySaveSingle(info)
	return info, nil
}

func industrySaveSingle(info *industryInfo) {
	industryMu.Lock()
	defer industryMu.Unlock()
	_, err := industryDB.Exec(
		"INSERT OR REPLACE INTO industry(code,name,industry,industry1,industry2,source,updated_at) VALUES(?,?,?,?,?,?,?)",
		info.Code, info.Name, info.Industry, info.Industry1, info.Industry2, info.Source, time.Now().Format(time.RFC3339))
	if err != nil {
		log.Printf("写入行业数据失败: %v", err)
	}
}

// handleGetIndustryCodes 返回已缓存行业的股票代码列表
func handleGetIndustryCodes(w http.ResponseWriter, r *http.Request) {
	rows, err := industryDB.Query("SELECT code FROM industry")
	if err != nil {
		errorResponse(w, "查询行业数据库失败: "+err.Error())
		return
	}
	defer rows.Close()
	codes := make([]string, 0)
	for rows.Next() {
		var code string
		if rows.Scan(&code) == nil {
			codes = append(codes, code)
		}
	}
	successResponse(w, map[string]interface{}{
		"count": len(codes),
		"list":  codes,
	})
}

// handleGetIndustry 获取股票所属行业（同花顺申万一级-二级）
func handleGetIndustry(w http.ResponseWriter, r *http.Request) {
	codeParam := strings.TrimSpace(r.URL.Query().Get("code"))
	if codeParam == "" {
		errorResponse(w, "股票代码不能为空")
		return
	}

	codes := strings.Split(codeParam, ",")
	results := make([]industryInfo, 0, len(codes))
	failed := make([]string, 0)

	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		var item industryInfo
		err := industryDB.QueryRow("SELECT code,name,industry,industry1,industry2,source FROM industry WHERE code=?", code).
			Scan(&item.Code, &item.Name, &item.Industry, &item.Industry1, &item.Industry2, &item.Source)
		if err == sql.ErrNoRows {
			fetched, fetchErr := industryFetchSingle(code)
			if fetchErr != nil || fetched == nil {
				industryMarkResult(code, false)
				failed = append(failed, code)
				continue
			}
			item = *fetched
		} else if err != nil {
			errorResponse(w, "查询行业数据库失败: "+err.Error())
			return
		}
		if item.Industry == "-" {
			item.Industry = ""
		}
		if item.Source == "" {
			item.Source = "ths"
		}
		results = append(results, item)
	}

	successResponse(w, map[string]interface{}{
		"count":     len(results),
		"list":      results,
		"not_found": failed,
	})
}
