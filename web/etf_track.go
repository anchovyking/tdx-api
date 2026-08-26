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
	"github.com/injoyai/tdx"
)

const (
	etfTrackDBPath      = "data/database/etf_track.db"
	etfTrackHTTPTimeout = 10 * time.Second
	etfTrackFetchDelay  = 1 * time.Second
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
			track_index TEXT,
			updated_at TIMESTAMP
		)`); err != nil {
			log.Printf("初始化ETF跟踪标的表失败: %v", err)
			return
		}
		etfTrackDB = db
		go etfTrackWarmLoop()
	})
}

// etfTrackWarmLoop 启动后自动补齐缺失的跟踪标的数据，之后每天检查一次新增ETF
func etfTrackWarmLoop() {
	time.Sleep(90 * time.Second)
	for {
		missing, err := etfTrackMissingCodes()
		if err != nil {
			log.Printf("ETF跟踪标的预热检查失败: %v", err)
		} else if len(missing) == 0 {
			log.Printf("ETF跟踪标的数据已全部缓存")
		} else {
			log.Printf("ETF跟踪标的预热开始，待补 %d 只", len(missing))
			ok := 0
			start := time.Now()
			for i, code := range missing {
				if _, err := fetchEtfTrack(code); err != nil {
					log.Printf("ETF跟踪标的预热[%d/%d] %s 失败: %v", i+1, len(missing), code, err)
				} else {
					ok++
				}
				if (i+1)%100 == 0 {
					log.Printf("ETF跟踪标的预热进度 %d/%d，成功 %d", i+1, len(missing), ok)
				}
				time.Sleep(etfTrackFetchDelay)
			}
			log.Printf("ETF跟踪标的预热完成，成功 %d/%d，耗时 %.0f 分钟", ok, len(missing), time.Since(start).Minutes())
		}
		time.Sleep(24 * time.Hour)
	}
}

// etfTrackMissingCodes 全市场ETF代码减去已缓存代码
func etfTrackMissingCodes() ([]string, error) {
	if tdx.DefaultCodes == nil {
		return nil, fmt.Errorf("代码缓存未初始化")
	}
	all := tdx.DefaultCodes.GetETFs()
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
	for _, fullCode := range all {
		code := fullCode
		if len(code) > 2 {
			code = code[2:]
		}
		if !cached[code] {
			missing = append(missing, code)
		}
	}
	return missing, nil
}

type etfTrackInfo struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	TrackIndex string `json:"track_index"`
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
		return nil, fmt.Errorf("未解析到跟踪标的数据")
	}
	info := &etfTrackInfo{Code: code, TrackIndex: strings.TrimSpace(m[1])}
	nameRe := regexp.MustCompile(`基金简称</th><td[^>]*>([^<]+)</td>`)
	if nm := nameRe.FindStringSubmatch(html); nm != nil {
		info.Name = strings.TrimSpace(nm[1])
	}

	etfTrackMu.Lock()
	defer etfTrackMu.Unlock()
	_, err = etfTrackDB.Exec(
		"INSERT OR REPLACE INTO etf_track(code,name,track_index,updated_at) VALUES(?,?,?,?)",
		info.Code, info.Name, info.TrackIndex, time.Now().Format(time.RFC3339))
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
		err := etfTrackDB.QueryRow("SELECT code,name,track_index FROM etf_track WHERE code=?", code).
			Scan(&item.Code, &item.Name, &item.TrackIndex)
		if err == sql.ErrNoRows {
			fetched, fetchErr := fetchEtfTrack(code)
			if fetchErr != nil || fetched == nil {
				failed = append(failed, code)
				continue
			}
			item = *fetched
		} else if err != nil {
			errorResponse(w, "查询ETF跟踪标的数据库失败: "+err.Error())
			return
		}
		results = append(results, item)
	}

	successResponse(w, map[string]interface{}{
		"count":     len(results),
		"list":      results,
		"not_found": failed,
	})
}
