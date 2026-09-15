package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/injoyai/tdx"
)

var notifyCfg tdx.NotifyConfig

var notifyHTTP = &http.Client{Timeout: 10 * time.Second}

// notifyTask 任务单次结束推送企业微信；推送失败只记日志，不影响任务
func notifyTask(name, trigger string, err error, detail string) {
	if !notifyCfg.Enabled {
		return
	}
	if err == nil && notifyCfg.OnlyFail {
		return
	}
	if notifyCfg.WecomWebhook == "" {
		return
	}
	status := "成功"
	if err != nil {
		status = "失败"
		if detail == "" {
			detail = err.Error()
		}
	}
	content := fmt.Sprintf("[tdx-api] 任务%s(%s)%s\n%s\n%s",
		name, trigger, status, detail, time.Now().Format("2006-01-02 15:04:05"))
	body, _ := json.Marshal(map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": content},
	})
	resp, postErr := notifyHTTP.Post(notifyCfg.WecomWebhook, "application/json", bytes.NewReader(body))
	if postErr != nil {
		log.Printf("企微通知推送失败(%s): %v", name, postErr)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("企微通知推送失败(%s): http状态码 %d", name, resp.StatusCode)
	}
}
