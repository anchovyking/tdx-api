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
	Code   string `json:"code"`
	Name   string `json:"name"`
	Market string `json:"market"`
	Source string `json:"source"`
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
// 参数: code (单个或逗号分隔), market (市场过滤), search (按名称模糊搜索), limit, offset
func handleGetIndices(w http.ResponseWriter, r *http.Request) {
	if indicesDB == nil {
		errorResponse(w, "指数数据库未初始化")
		return
	}

	q := r.URL.Query()
	codeParam := strings.TrimSpace(q.Get("code"))
	marketParam := strings.TrimSpace(q.Get("market"))
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
	if marketParam != "" {
		markets := strings.Split(marketParam, ",")
		placeholders := ""
		for i, m := range markets {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, strings.TrimSpace(m))
		}
		conditions = append(conditions, "market IN ("+placeholders+")")
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
	query := `SELECT code, name, market, source FROM indices` + where +
		" ORDER BY code, market LIMIT ? OFFSET ?"
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
		var name, market, source sql.NullString
		if err := rows.Scan(&idx.Code, &name, &market, &source); err != nil {
			continue
		}
		idx.Name = name.String
		idx.Market = market.String
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
