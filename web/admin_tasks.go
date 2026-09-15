package main

import (
	"net/http"
	"sort"
	"strings"
)

// handleAdminTaskRun 手动触发一次任务
// POST /api/admin/tasks/run?name=industry|etf|excal|codes|workday|indices&codes=..(仅excal用,≤50)
func handleAdminTaskRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, "只支持POST请求")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		errorResponse(w, "name 不能为空")
		return
	}
	arg := strings.TrimSpace(r.URL.Query().Get("codes"))
	detail, err := schedRun(name, "manual", arg)
	if err != nil {
		errorResponse(w, err.Error())
		return
	}
	successResponse(w, map[string]interface{}{"name": name, "detail": detail})
}

// handleAdminTaskStatus 查询各任务开关/cron/上次执行情况
// GET /api/admin/tasks/status
func handleAdminTaskStatus(w http.ResponseWriter, r *http.Request) {
	schedMu.Lock()
	list := make([]*taskStatus, 0, len(schedTasks))
	for _, st := range schedTasks {
		cp := *st
		list = append(list, &cp)
	}
	schedMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	successResponse(w, map[string]interface{}{
		"count": len(list),
		"list":  list,
	})
}
