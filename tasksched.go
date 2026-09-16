package tdx

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

// TaskConfig 单个任务的调度配置
type TaskConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Cron       string `yaml:"cron"`
	RunAtStart bool   `yaml:"run_at_start"`
}

// NotifyConfig 任务结果通知配置
type NotifyConfig struct {
	Enabled      bool   `yaml:"enabled"`
	WecomWebhook string `yaml:"wecom_webhook"`
	OnlyFail     bool   `yaml:"only_fail"`
}

// SyncConfig 抓取/同步性能参数（可在线调，无需改代码）
type SyncConfig struct {
	// ExcalConcurrency xdxr 排期全量的并发只数（默认 4）。调大可显著提速，
	// 但对 TDX 服务器压力更大；建议 4~32。
	ExcalConcurrency int `yaml:"excal_concurrency"`
}

// SchedulerConfig 调度总配置，对应 config.yaml
type SchedulerConfig struct {
	Tasks  map[string]TaskConfig `yaml:"tasks"`
	Notify NotifyConfig          `yaml:"notify"`
	Sync   SyncConfig            `yaml:"sync"`
}

// 内置默认（config.yaml 缺失/未填时的回退）
const (
	DefaultExcalConcurrency = 4
	// 并发上限，防止填成 9999 直接把 TDX 打爆
	MaxExcalConcurrency = 64
)

// EffectiveExcalConcurrency 返回生效的 xdxr 全量并发度；<=0 用默认，超过上限截断
func EffectiveExcalConcurrency(n int) int {
	if n <= 0 {
		return DefaultExcalConcurrency
	}
	if n > MaxExcalConcurrency {
		return MaxExcalConcurrency
	}
	return n
}

// 各任务在代码内置的默认值（config.yaml 缺失/解析失败时的回退，与仓库 config.yaml 一致）
var defaultTaskConfigs = map[string]TaskConfig{
	"codes":   {Enabled: true, Cron: "10 0 9 * * *", RunAtStart: false},
	"workday": {Enabled: true, Cron: "0 0 9 * * *", RunAtStart: false},
	"industry": {Enabled: true, Cron: "0 0 3 * * *", RunAtStart: false},
	"etf":      {Enabled: true, Cron: "0 0 4 * * *", RunAtStart: false},
	"excal":    {Enabled: true, Cron: "0 0 2 * * *", RunAtStart: false},
	"indices":  {Enabled: true, Cron: "20 0 9 * * *", RunAtStart: true},
}

// TaskDoneHook 任务单次执行结束后的回调（trigger: cron/start/manual）
// 由 web 层设置为通知+状态记录；库内仅在非空时调用
var TaskDoneHook func(name, trigger string, err error, detail string)

func emitTaskDone(name, trigger string, err error, detail string) {
	if TaskDoneHook != nil {
		TaskDoneHook(name, trigger, err, detail)
	}
}

// LoadTaskConfig 加载调度配置：文件 > 环境变量覆盖 > 内置默认
// path 不存在或解析失败时记日志并返回内置默认
func LoadTaskConfig(path string) *SchedulerConfig {
	cfg := &SchedulerConfig{
		Tasks:  make(map[string]TaskConfig, len(defaultTaskConfigs)),
		Notify: NotifyConfig{Enabled: true, OnlyFail: false},
		Sync:   SyncConfig{ExcalConcurrency: DefaultExcalConcurrency},
	}
	for k, v := range defaultTaskConfigs {
		cfg.Tasks[k] = v
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		log.Printf("调度配置文件 %s 未找到(%v)，使用内置默认", path, err)
	} else if err := yaml.Unmarshal(bs, cfg); err != nil {
		log.Printf("调度配置文件 %s 解析失败(%v)，使用内置默认", path, err)
		for k, v := range defaultTaskConfigs {
			cfg.Tasks[k] = v
		}
		cfg.Notify = NotifyConfig{Enabled: true, OnlyFail: false}
		cfg.Sync = SyncConfig{ExcalConcurrency: DefaultExcalConcurrency}
	}
	// 环境变量覆盖单个任务
	for name := range defaultTaskConfigs {
		t := cfg.Tasks[name]
		prefix := "TASK_" + strings.ToUpper(name) + "_"
		if v := strings.TrimSpace(os.Getenv(prefix + "ENABLED")); v != "" {
			t.Enabled = parseBoolEnv(v, t.Enabled)
		}
		if v := strings.TrimSpace(os.Getenv(prefix + "CRON")); v != "" {
			t.Cron = v
		}
		if v := strings.TrimSpace(os.Getenv(prefix + "RUN_AT_START")); v != "" {
			t.RunAtStart = parseBoolEnv(v, t.RunAtStart)
		}
		cfg.Tasks[name] = t
	}
	// 通知：WECOM_WEBHOOK（全地址）优先，其次文件内的 wecom_webhook
	if v := strings.TrimSpace(os.Getenv("WECOM_WEBHOOK")); v != "" {
		cfg.Notify.WecomWebhook = v
	}
	if v := strings.TrimSpace(os.Getenv("NOTIFY_ENABLED")); v != "" {
		cfg.Notify.Enabled = parseBoolEnv(v, cfg.Notify.Enabled)
	}
	if v := strings.TrimSpace(os.Getenv("NOTIFY_ONLY_FAIL")); v != "" {
		cfg.Notify.OnlyFail = parseBoolEnv(v, cfg.Notify.OnlyFail)
	}
	// 抓取并发：EXCAL_CONCURRENCY 优先
	if v := strings.TrimSpace(os.Getenv("EXCAL_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Sync.ExcalConcurrency = n
		} else {
			log.Printf("EXCAL_CONCURRENCY(%s) 非整数，忽略", v)
		}
	}
	return cfg
}

func parseBoolEnv(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// FormatDuration 耗时统一格式：≥1小时→"1小时35分"，≥1分钟→"4分11秒"，"12分"，"8秒"
func FormatDuration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 0 {
		s = 0
	}
	h, m, sec := s/3600, (s%3600)/60, s%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%d小时%d分", h, m)
	case h > 0:
		return fmt.Sprintf("%d小时", h)
	case m > 0 && sec > 0:
		return fmt.Sprintf("%d分%d秒", m, sec)
	case m > 0:
		return fmt.Sprintf("%d分", m)
	default:
		return fmt.Sprintf("%d秒", sec)
	}
}

// SqliteDSN 给 sqlite 文件路径加上并发友好的 DSN 参数：
// _busy_timeout=10000 撞锁等 10 秒而不是立即 SQLITE_BUSY；
// _journal_mode=WAL 读不再阻塞写。
// 同一进程多实例同写一库、CLI 查询撞上写入都不再直接报错。
func SqliteDSN(path string) string {
	if strings.Contains(path, "?") {
		return path
	}
	return path + "?_busy_timeout=10000&_journal_mode=WAL"
}

// cronParser 6 段含秒的解析器，与各处 cron.New(cron.WithSeconds()) 一致
func cronParser() cron.Parser {
	return cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
}

// EffectiveCron 返回生效的 cron；非法时记日志并回退该任务的内置默认
// cron 为空（indices 这类无循环任务）返回 ""，调用方不注册定时
func EffectiveCron(name, spec string) string {
	if strings.TrimSpace(spec) == "" {
		return ""
	}
	if err := ValidCron(spec); err != nil {
		def := defaultTaskConfigs[name].Cron
		log.Printf("任务 %s 的 cron(%s) 非法(%v)，回退默认(%s)", name, spec, err, def)
		return def
	}
	return spec
}

// ValidCron 校验 cron 表达式（6 段含秒），非法返回 error
func ValidCron(spec string) error {
	if strings.TrimSpace(spec) == "" {
		return fmt.Errorf("cron 表达式为空")
	}
	p := cronParser()
	_, err := p.Parse(spec)
	return err
}
