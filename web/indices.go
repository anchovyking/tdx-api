package main

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"sync"

	_ "github.com/glebarez/go-sqlite"
)

const indicesDBPath = "data/database/indices.db"

var (
	indicesDB   *sql.DB
	indicesMu   sync.RWMutex
	indicesOnce sync.Once
)

// IndexInfo 指数信息
type IndexInfo struct {
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	PublishDate    string  `json:"publish_date"`
	CurrentLevel   float64 `json:"current_level"`
	YearReturn     float64 `json:"year_return"`
	Category       string  `json:"category"`
	PeTtmCurrent   float64 `json:"pe_ttm_current"`
	PeTtmPercentile float64 `json:"pe_ttm_percentile"`
	PeTtmAvg       float64 `json:"pe_ttm_avg"`
	PeTtmMin       float64 `json:"pe_ttm_min"`
	PeTtmP20       float64 `json:"pe_ttm_p20"`
	PeTtmP50       float64 `json:"pe_ttm_p50"`
	PeTtmP80       float64 `json:"pe_ttm_p80"`
	PeTtmMax       float64 `json:"pe_ttm_max"`
	PbCurrent      float64 `json:"pb_current"`
	PbPercentile   float64 `json:"pb_percentile"`
	PbAvg          float64 `json:"pb_avg"`
	PbMin          float64 `json:"pb_min"`
	PbP20          float64 `json:"pb_p20"`
	PbP50          float64 `json:"pb_p50"`
	PbP80          float64 `json:"pb_p80"`
	PbMax          float64 `json:"pb_max"`
	PsCurrent      float64 `json:"ps_current"`
	PsPercentile   float64 `json:"ps_percentile"`
	PsAvg          float64 `json:"ps_avg"`
	PsMin          float64 `json:"ps_min"`
	PsP20          float64 `json:"ps_p20"`
	PsP50          float64 `json:"ps_p50"`
	PsP80          float64 `json:"ps_p80"`
	PsMax          float64 `json:"ps_max"`
	Roe2025        float64 `json:"roe_2025"`
	Roe2024        float64 `json:"roe_2024"`
	Roe2023        float64 `json:"roe_2023"`
	DividendYield  float64 `json:"dividend_yield"`
	MarketCap      string  `json:"market_cap"`
	Note           string  `json:"note"`
	Source         string  `json:"source"`
}

// InitIndices 初始化指数数据库连接
func InitIndices() {
	indicesOnce.Do(func() {
		db, err := sql.Open("sqlite", indicesDBPath)
		if err != nil {
			log.Printf("打开指数数据库失败: %v", err)
			return
		}
		// 检查表是否存在
		var name string
		if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='indices'").Scan(&name); err != nil {
			log.Printf("指数表不存在，请先运行 scripts/import_indices.py 导入数据: %v", err)
			db.Close()
			return
		}
		indicesDB = db
	})
}

// handleGetIndices 查询指数列表
// GET /api/indices
// 参数: code (单个或逗号分隔), search (按名称模糊搜索), limit, offset
func handleGetIndices(w http.ResponseWriter, r *http.Request) {
	if indicesDB == nil {
		errorResponse(w, "指数数据库未初始化")
		return
	}

	q := r.URL.Query()
	codeParam := strings.TrimSpace(q.Get("code"))
	search := strings.TrimSpace(q.Get("search"))
	limit := parsePositiveInt(q.Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := parsePositiveInt(q.Get("offset"))

	conditions := []string{}
	args := []interface{}{}
	if codeParam != "" {
		codes := strings.Split(codeParam, ",")
		placeholders := ""
		for i, c := range codes {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, strings.TrimSpace(c))
		}
		conditions = append(conditions, "code IN ("+placeholders+")")
	}
	if search != "" {
		conditions = append(conditions, "name LIKE ?")
		args = append(args, "%"+search+"%")
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	// 查询总数
	var total int
	countQuery := "SELECT COUNT(*) FROM indices" + where
	if err := indicesDB.QueryRow(countQuery, args...).Scan(&total); err != nil {
		errorResponse(w, "查询失败: "+err.Error())
		return
	}

	// 查询数据
	query := `SELECT code, name, publish_date, current_level, year_return, category,
		pe_ttm_current, pe_ttm_percentile, pe_ttm_avg, pe_ttm_min,
		pe_ttm_p20, pe_ttm_p50, pe_ttm_p80, pe_ttm_max,
		pb_current, pb_percentile, pb_avg, pb_min,
		pb_p20, pb_p50, pb_p80, pb_max,
		ps_current, ps_percentile, ps_avg, ps_min,
		ps_p20, ps_p50, ps_p80, ps_max,
		roe_2025, roe_2024, roe_2023,
		dividend_yield, market_cap, note, source
		FROM indices` + where + " ORDER BY code LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := indicesDB.Query(query, args...)
	if err != nil {
		errorResponse(w, "查询失败: "+err.Error())
		return
	}
	defer rows.Close()

	list := make([]IndexInfo, 0, limit)
	for rows.Next() {
		var idx IndexInfo
		var publishDate, name, category, note, source sql.NullString
		if err := rows.Scan(
			&idx.Code, &name, &publishDate, &idx.CurrentLevel, &idx.YearReturn, &category,
			&idx.PeTtmCurrent, &idx.PeTtmPercentile, &idx.PeTtmAvg, &idx.PeTtmMin,
			&idx.PeTtmP20, &idx.PeTtmP50, &idx.PeTtmP80, &idx.PeTtmMax,
			&idx.PbCurrent, &idx.PbPercentile, &idx.PbAvg, &idx.PbMin,
			&idx.PbP20, &idx.PbP50, &idx.PbP80, &idx.PbMax,
			&idx.PsCurrent, &idx.PsPercentile, &idx.PsAvg, &idx.PsMin,
			&idx.PsP20, &idx.PsP50, &idx.PsP80, &idx.PsMax,
			&idx.Roe2025, &idx.Roe2024, &idx.Roe2023,
			&idx.DividendYield, &idx.MarketCap, &note, &source,
		); err != nil {
			continue
		}
		idx.Name = name.String
		idx.PublishDate = publishDate.String
		idx.Category = category.String
		idx.Note = note.String
		idx.Source = source.String
		list = append(list, idx)
	}

	successResponse(w, map[string]interface{}{
		"total":  total,
		"count":  len(list),
		"list":   list,
		"limit":  limit,
		"offset": offset,
	})
}
