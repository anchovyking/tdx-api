package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/injoyai/tdx"
	"github.com/robfig/cron/v3"
)

// 统一任务调度：cron 注册 + 手动触发 + 状态记录 + 结果通知
// 配置见 config.yaml，由 server.go:init() 最先加载到 schedCfg

type taskStatus struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Cron       string `json:"cron"`
	RunAtStart bool   `json:"run_at_start"`
	LastTrigger string `json:"last_trigger"`
	LastStart   string `json:"last_start"`
	LastEnd     string `json:"last_end"`
	LastOK      *bool  `json:"last_ok"`
	LastDetail  string `json:"last_detail"`
	running     bool
}

var (
	schedCfg   *tdx.SchedulerConfig
	schedCron  = cron.New(cron.WithSeconds())
	schedMu    sync.Mutex
	schedTasks = map[string]*taskStatus{}
	// run-once 函数：trigger=cron/start/manual，arg 仅 excal 手动补数用（逗号分隔代码）
	schedFuncs = map[string]func(trigger, arg string) (string, error){}
	// 启动跑的错峰延迟，防打满 TDX 连接池
	schedStartDelay = map[string]time.Duration{
		"industry": 60 * time.Second,
		"etf":      90 * time.Second,
		"excal":    5 * time.Minute,
		"indices":  30 * time.Second,
		"codes":    0,
		"workday":  0,
	}
)

// schedHook 供 tdx.TaskDoneHook：codes/workday（根包内跑）的结果回流到状态+通知
func schedHook(name, trigger string, err error, detail string) {
	schedMu.Lock()
	st, ok := schedTasks[name]
	if !ok {
		t := tdx.TaskConfig{}
		if schedCfg != nil {
			t = schedCfg.Tasks[name]
		}
		st = &taskStatus{Name: name, Enabled: t.Enabled, Cron: t.Cron, RunAtStart: t.RunAtStart}
		schedTasks[name] = st
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	st.LastTrigger = trigger
	st.LastEnd = now
	okv := err == nil
	st.LastOK = &okv
	st.LastDetail = detail
	if err != nil && detail == "" {
		st.LastDetail = err.Error()
	}
	schedMu.Unlock()
	notifyTask(name, trigger, err, st.LastDetail)
}

// schedRegisterManual 仅注册手动入口与状态位（cron/启动由别处负责，如根包内的 codes/workday）
func schedRegisterManual(name string, fn func(trigger, arg string) (string, error)) {
	t := schedCfg.Tasks[name]
	schedMu.Lock()
	schedTasks[name] = &taskStatus{Name: name, Enabled: t.Enabled, Cron: t.Cron, RunAtStart: t.RunAtStart}
	schedFuncs[name] = fn
	schedMu.Unlock()
	log.Printf("任务 %s 已注册：enabled=%v cron=%s run_at_start=%v", name, t.Enabled, t.Cron, t.RunAtStart)
}

// schedRegister 注册 web 侧任务：cron 定时 + 启动跑（按配置）
func schedRegister(name string, fn func(trigger, arg string) (string, error)) {
	schedRegisterManual(name, fn)
	t := schedCfg.Tasks[name]
	if !t.Enabled {
		log.Printf("任务 %s 已停用(enabled=false)", name)
		return
	}
	if spec := tdx.EffectiveCron(name, t.Cron); spec != "" {
		if _, err := schedCron.AddFunc(spec, func() {
			_, _ = schedRun(name, "cron", "")
		}); err != nil {
			log.Printf("任务 %s 注册 cron(%s) 失败: %v", name, spec, err)
		} else {
			log.Printf("任务 %s cron 已注册: %s", name, spec)
		}
	}
	if t.RunAtStart {
		delay := schedStartDelay[name]
		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			_, _ = schedRun(name, "start", "")
		}()
	}
}

// schedRun 执行一次任务：防重叠（上一轮未结束则跳过）+ 状态 + 通知
func schedRun(name, trigger, arg string) (string, error) {
	schedMu.Lock()
	st, ok := schedTasks[name]
	fn := schedFuncs[name]
	if !ok || fn == nil {
		schedMu.Unlock()
		return "", fmt.Errorf("未知任务: %s", name)
	}
	if !st.Enabled {
		schedMu.Unlock()
		return "", fmt.Errorf("任务 %s 已停用", name)
	}
	if st.running {
		schedMu.Unlock()
		return "", fmt.Errorf("任务 %s 上一轮未结束，跳过", name)
	}
	st.running = true
	st.LastTrigger = trigger
	st.LastStart = time.Now().Format("2006-01-02 15:04:05")
	schedMu.Unlock()

	detail, err := fn(trigger, arg)

	okv := err == nil
	schedMu.Lock()
	st.running = false
	st.LastEnd = time.Now().Format("2006-01-02 15:04:05")
	st.LastOK = &okv
	st.LastDetail = detail
	if err != nil && detail == "" {
		st.LastDetail = err.Error()
	}
	detailOut := st.LastDetail
	schedMu.Unlock()
	notifyTask(name, trigger, err, detailOut)
	return detailOut, err
}

// schedStart 启动 cron（main 中各 Init 注册完成后调用一次）
func schedStart() {
	schedCron.Start()
	log.Printf("统一调度已启动")
}
