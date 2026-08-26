package main

import (
	"context"
	"database/sql"
	"errors"
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
	"github.com/injoyai/tdx"
	"github.com/injoyai/tdx/protocol"
)

const (
	etfTrackDBPath      = "data/database/etf_track.db"
	etfTrackHTTPTimeout = 10 * time.Second
	etfTrackFetchDelay  = 2500 * time.Millisecond
)

var (
	etfTrackDB   *sql.DB
	etfTrackMu   sync.Mutex
	etfTrackOnce sync.Once
)

// InitEtfTrack 初始化ETF跟踪标的存储并启动后台预热
func InitEtfTrack() {
	etfTrackOnce.Do(func() {
		db, err := sql.Open("sqlite", etfTrackDBPath)
		if err != nil {
			log.Printf("打开ETF跟踪标的数据库失败: %v", err)
			return
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS etf_track (
			code       TEXT PRIMARY KEY,
			name       TEXT,
			market     TEXT DEFAULT '',
			track_index TEXT,
			updated_at TIMESTAMP
		); CREATE TABLE IF NOT EXISTS etf_track_failed (
			code      TEXT PRIMARY KEY,
			fails     INTEGER DEFAULT 0,
			last_fail TIMESTAMP
		)`); err != nil {
			log.Printf("初始化ETF跟踪标的表失败: %v", err)
			return
		}
		db.Exec("ALTER TABLE etf_track ADD COLUMN market TEXT DEFAULT ''")
		etfTrackDB = db
		go etfTrackWarmLoop()
	})
}

// etfTrackWarmLoop 启动后自动补齐缺失的跟踪标的数据，之后每天检查一次新增ETF
func etfTrackWarmLoop() {
	time.Sleep(90 * time.Second)
	for {
		warmMu.Lock()
		missing, err := etfTrackMissingCodes()
		if err != nil {
			log.Printf("ETF跟踪标的预热检查失败: %v", err)
		} else if len(missing) == 0 {
			log.Printf("ETF跟踪标的数据已全部缓存")
		} else {
			log.Printf("ETF跟踪标的预热开始，待补 %d 只", len(missing))
			ok := 0
			failCount := 0
			consecutiveFails := 0
			start := time.Now()
			for i, code := range missing {
				_, fetchErr := fetchEtfTrack(code)
				if fetchErr != nil {
					failCount++
					log.Printf("ETF跟踪标的预热[%d/%d] %s 失败: %v", i+1, len(missing), code, fetchErr)
					// 仅网络类错误触发熔断；数据源无档案的代码不暂停
					if !errors.Is(fetchErr, errTrackNotFound) {
						consecutiveFails++
						if consecutiveFails >= 5 {
							log.Printf("ETF预热连续失败%d次，暂停5分钟防限流", consecutiveFails)
							time.Sleep(5 * time.Minute)
							consecutiveFails = 0
						}
					}
				} else {
					ok++
					consecutiveFails = 0
				}
				etfTrackMarkResult(code, fetchErr == nil)
				if (i+1)%5 == 0 {
					log.Printf("ETF跟踪标的预热进度 %d/%d 成功%d 失败%d 已耗时%.0f分钟",
						i+1, len(missing), ok, failCount, time.Since(start).Minutes())
				}
				time.Sleep(etfTrackFetchDelay)
			}
			log.Printf("ETF跟踪标的预热完成，成功 %d/%d，耗时 %.0f 分钟", ok, len(missing), time.Since(start).Minutes())
		}
		warmMu.Unlock()
		time.Sleep(24 * time.Hour)
	}
}

// etfTrackMissingCodes 全市场ETF代码减去已缓存代码（代码/名称/交易所取自codes.db）
func etfTrackMissingCodes() ([]string, error) {
	models, err := getAllCodeModels()
	if err != nil {
		return nil, err
	}
	cached := make(map[string]bool)
	rows, err := etfTrackDB.Query("SELECT code FROM etf_track")
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
	for _, model := range models {
		fullCode := model.FullCode()
		if !protocol.IsETF(fullCode) {
			continue
		}
		if !cached[model.Code] && !etfTrackSkipFailed(model.Code, now) {
			missing = append(missing, model.Code)
		}
	}
	return missing, nil
}

// etfTrackSkipFailed 失败≥3次且30天内不再重试
func etfTrackSkipFailed(code string, now time.Time) bool {
	var fails int
	var lastFail string
	if err := etfTrackDB.QueryRow(
		"SELECT fails,last_fail FROM etf_track_failed WHERE code=?", code).
		Scan(&fails, &lastFail); err != nil {
		return false
	}
	if fails < 3 {
		return false
	}
	t, _ := time.Parse(time.RFC3339, lastFail)
	return now.Sub(t) < 30*24*time.Hour
}

// etfTrackMarkResult 记录成功（清除失败记录）或失败（累加次数）
func etfTrackMarkResult(code string, success bool) {
	if success {
		etfTrackDB.Exec("DELETE FROM etf_track_failed WHERE code=?", code)
		return
	}
	etfTrackDB.Exec(`INSERT INTO etf_track_failed(code,fails,last_fail) VALUES(?,1,?)
		ON CONFLICT(code) DO UPDATE SET fails=fails+1, last_fail=excluded.last_fail`,
		code, time.Now().Format(time.RFC3339))
}

type etfTrackInfo struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Market     string `json:"market"`
	TrackIndex string `json:"track_index"`
}

// etfCodeModel 从codes.db查ETF的代码模型（名称、交易所）
func etfCodeModel(code string) *tdx.CodeModel {
	if tdx.DefaultCodes == nil {
		return nil
	}
	if m := tdx.DefaultCodes.Get("sh" + code); m != nil {
		return m
	}
	return tdx.DefaultCodes.Get("sz" + code)
}

var etfTrackHTTPClient = &http.Client{
	Timeout: etfTrackHTTPTimeout,
	Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, "tcp4", addr)
		},
	},
}

// errTrackNotFound 数据源无此基金档案（未上市占位代码等），非网络错误
var errTrackNotFound = errors.New("数据源无此基金的跟踪标的数据")

// fetchEtfTrack 从东财基金F10抓取跟踪标的（永久缓存）
func fetchEtfTrack(code string) (*etfTrackInfo, error) {
	url := fmt.Sprintf("http://fundf10.eastmoney.com/jbgk_%s.html", code)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := etfTrackHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http状态码: %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	html := string(raw)

	trackRe := regexp.MustCompile(`跟踪标的</th><td[^>]*>([^<]+)</td>`)
	m := trackRe.FindStringSubmatch(html)
	if m == nil {
		return nil, errTrackNotFound
	}
	info := &etfTrackInfo{Code: code, TrackIndex: strings.TrimSpace(m[1])}
	if model := etfCodeModel(code); model != nil {
		info.Name = model.Name
		info.Market = strings.ToLower(model.Exchange)
	}

	etfTrackMu.Lock()
	defer etfTrackMu.Unlock()
	_, err = etfTrackDB.Exec(
		"INSERT OR REPLACE INTO etf_track(code,name,market,track_index,updated_at) VALUES(?,?,?,?,?)",
		info.Code, info.Name, info.Market, info.TrackIndex, time.Now().Format(time.RFC3339))
	if err != nil {
		log.Printf("写入ETF跟踪标的数据失败: %v", err)
	}
	return info, nil
}

// handleGetEtfTrack 获取ETF跟踪标的指数（东财基金F10，永久缓存）
func handleGetEtfTrack(w http.ResponseWriter, r *http.Request) {
	codeParam := strings.TrimSpace(r.URL.Query().Get("code"))
	if codeParam == "" {
		errorResponse(w, "基金代码不能为空")
		return
	}

	codes := strings.Split(codeParam, ",")
	results := make([]etfTrackInfo, 0, len(codes))
	failed := make([]string, 0)

	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		var item etfTrackInfo
		err := etfTrackDB.QueryRow("SELECT code,name,market,track_index FROM etf_track WHERE code=?", code).
			Scan(&item.Code, &item.Name, &item.Market, &item.TrackIndex)
		if err == sql.ErrNoRows {
			fetched, fetchErr := fetchEtfTrack(code)
			if fetchErr != nil || fetched == nil {
				etfTrackMarkResult(code, false)
				failed = append(failed, code)
				continue
			}
			item = *fetched
		} else if err != nil {
			errorResponse(w, "查询ETF跟踪标的数据库失败: "+err.Error())
			return
		}
		if item.Market == "" {
			if model := etfCodeModel(code); model != nil {
				item.Name = model.Name
				item.Market = strings.ToLower(model.Exchange)
				etfTrackMu.Lock()
				etfTrackDB.Exec("UPDATE etf_track SET name=?, market=? WHERE code=?",
					item.Name, item.Market, code)
				etfTrackMu.Unlock()
			}
		}
		results = append(results, item)
	}

	successResponse(w, map[string]interface{}{
		"count":     len(results),
		"list":      results,
		"not_found": failed,
	})
}
