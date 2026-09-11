package main

import (
	"net/http"
	"strings"

	"github.com/injoyai/tdx/protocol"
)

// xdxrItem 单股除权除息返回体
type xdxrItem struct {
	Code    string                 `json:"code"`
	Records []*protocol.XdxrRecord `json:"records"`
}

// handleGetXdxr 获取除权除息/股本变迁全历史(通达信0x000f,无缓存,实时查询)
// 示例: GET /api/xdxr?code=600519
//      GET /api/xdxr?code=600519,000001
func handleGetXdxr(w http.ResponseWriter, r *http.Request) {
	codeParam := strings.TrimSpace(r.URL.Query().Get("code"))
	if codeParam == "" {
		errorResponse(w, "股票代码不能为空")
		return
	}

	codes := splitCodes(codeParam)
	if len(codes) == 0 {
		errorResponse(w, "股票代码不能为空")
		return
	}
	if len(codes) > 50 {
		errorResponse(w, "一次最多查询50只股票")
		return
	}

	results := make([]xdxrItem, 0, len(codes))
	failed := make([]string, 0)
	for _, code := range codes {
		resp, err := client.GetXdxrInfo(code)
		if err != nil {
			failed = append(failed, code)
			continue
		}
		if resp.Records == nil {
			resp.Records = []*protocol.XdxrRecord{}
		}
		results = append(results, xdxrItem{Code: resp.Code, Records: resp.Records})
	}

	successResponse(w, map[string]interface{}{
		"count":     len(results),
		"list":      results,
		"not_found": failed,
	})
}
