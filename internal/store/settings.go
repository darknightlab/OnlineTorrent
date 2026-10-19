package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/darknightlab/onlinetorrent/internal/rank"
)

// SettingSpec describes one admin-editable setting. Values are stored as JSON.
type SettingSpec struct {
	Key         string   `json:"key"`
	Group       string   `json:"group"`
	Type        string   `json:"type"` // string|text|markdown|number|bool|enum|string_list|secret
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Options     []string `json:"options,omitempty"`
	Default     any      `json:"-"`
	Public      bool     `json:"-"` // exposed via /site
	// Hidden settings (type json) are edited on their own admin page and its endpoint,
	// not in GET/PUT /admin/settings.
	Hidden bool `json:"-"`
	// Validate normalises a json setting (returns the value to store).
	Validate func(json.RawMessage) (json.RawMessage, error) `json:"-"`
}

// Setting keys.
const (
	KeySiteName          = "site.name"
	KeySiteBaseURL       = "site.base_url"
	KeyAnnouncement      = "site.announcement_md"
	KeyRegistration      = "auth.registration"
	KeySessionDays       = "auth.session_days"
	KeyTurnstileSiteKey  = "auth.turnstile_site_key"
	KeyTurnstileSecret   = "auth.turnstile_secret"
	KeyTrackers          = "webtorrent.trackers"
	KeyBTTrackers        = "bt.trackers"
	KeySwarmEnabled      = "swarm.enabled"
	KeyRankRules         = "rank.rules"
	KeyICEServers        = "webtorrent.ice_servers"
	KeyMaxAutoSize       = "play.max_auto_size_mb"
	KeyMaxPushSize       = "play.max_push_size_mb"
	KeyDefaultAction     = "policy.default_action"
	KeyAutoReviewUnknown = "policy.review_user_magnets"
	KeyUpstreamProxy     = "upstream.proxy"
	KeyMoeBase           = "upstream.bangumi_moe"
	KeyBangumiDataURL    = "upstream.bangumi_data_url"
	KeyBgmUserAgent      = "upstream.bgm_user_agent"
	KeyTMDBKey           = "upstream.tmdb_api_key"
	KeyCrawlEnabled      = "crawl.enabled"
	KeyBgmArchiveEnabled = "bgm_archive.enabled"
	KeyCrawlWorkers      = "crawl.workers"
	KeyCleanupEnabled    = "qb.cleanup_enabled"
	KeyQBAddTimeoutSec   = "qb.add_timeout_sec"
	KeyWebseedEnabled    = "webseed.enabled"
)

// Decision model (Cloudflare Workers AI Clef, internal/clef) settings.
const (
	KeyClefEnabled   = "clef.enabled"
	KeyClefAccountID = "clef.account_id"
	KeyClefToken     = "clef.api_token"
	KeyClefModel     = "clef.model"
	KeyClefBaseURL   = "clef.base_url"
	KeyClefMetric    = "clef.metric"
	KeyClefApply     = "clef.apply_threshold"
	KeyClefMargin    = "clef.apply_margin"
	KeyClefReview    = "clef.review_threshold"
	KeyClefRate      = "clef.rate_per_min"
	KeyClefDailyCap  = "clef.daily_cap"
	KeyClefProvider  = "clef.provider" // openai (LLM, default) | clef

	// LLM backend (OpenAI-compatible chat completions with structured output)
	KeyLLMBaseURL     = "llm.base_url"
	KeyLLMAPIKey      = "llm.api_key"
	KeyLLMModel       = "llm.model"
	KeyLLMTimeoutSec  = "llm.timeout_sec"
	KeyLLMConcurrency = "llm.concurrency"
	KeyLLMRate        = "llm.rate_per_min"
	KeyLLMDailyCap    = "llm.daily_cap"
	KeyLLMApply       = "llm.apply_threshold"
	KeyLLMReview      = "llm.review_threshold"
	KeyLLMAgreement   = "llm.require_agreement"
	KeyLLMEffort      = "llm.reasoning_effort"
)

// clefSettings: group 决策模型 (defaults calibrated, docs/research/decision-model-calibration.md).
func clefSettings() []SettingSpec {
	const g = "决策模型"
	return []SettingSpec{
		{Key: KeyClefEnabled, Group: g, Type: "bool", Label: "启用决策模型", Description: "规则无法确定的新收录种子文件，由决策模型在候选条目里选择（后台队列，只处理最近 3 天入队的种子）；关闭后「测试」仍可用", Default: false},
		{Key: KeyClefProvider, Group: g, Type: "enum", Label: "决策后端", Options: []string{"openai", "clef"}, Description: "openai = 兼容 OpenAI 的大模型（结构化输出，推荐）；clef = Cloudflare Workers AI 的 Clef", Default: "openai"},
		{Key: KeyLLMBaseURL, Group: g, Type: "string", Label: "LLM：API 地址", Description: "OpenAI 兼容接口（/chat/completions，需要 json_schema 结构化输出）", Default: "https://sub2api.projectk.org/v1"},
		{Key: KeyLLMAPIKey, Group: g, Type: "secret", Label: "LLM：API Key", Default: ""},
		{Key: KeyLLMModel, Group: g, Type: "string", Label: "LLM：模型", Default: "gpt-6-luna"},
		{Key: KeyLLMEffort, Group: g, Type: "enum", Label: "LLM：推理强度", Options: []string{"default", "minimal", "low", "medium", "high"}, Description: "reasoning_effort；default = 不发送（模型默认）。medium 在缺少正确候选时更愿意回答 null", Default: "medium"},
		{Key: KeyLLMTimeoutSec, Group: g, Type: "number", Label: "LLM：超时（秒）", Default: 120},
		{Key: KeyLLMConcurrency, Group: g, Type: "number", Label: "LLM：并发数", Description: "同时处理的种子数（1–8）", Default: 2},
		{Key: KeyLLMRate, Group: g, Type: "number", Label: "LLM：每分钟请求数", Default: 30},
		{Key: KeyLLMDailyCap, Group: g, Type: "number", Label: "LLM：每日请求上限", Description: "含后台测试；按 UTC 日期计", Default: 5000},
		{Key: KeyLLMApply, Group: g, Type: "number", Label: "LLM：直接应用阈值", Description: "模型自报置信度 ≥ 此值、校验通过（条目在候选中、集数在官方章节里、季数一致）、标题里有该条目的名字且与规则解析出的集数一致时直接写入映射；发布时间与本集播出日期不符（晚 30 天以上 / 早 7 天以上）或缺少发布时间的一律进入审核", Default: 0.9},
		{Key: KeyLLMReview, Group: g, Type: "number", Label: "LLM：人工审核阈值", Description: "校验通过但置信度介于此值与直接应用之间（或与解析的集数不一致）的进入审核队列；低于此值不映射", Default: 0.5},
		{Key: KeyLLMAgreement, Group: g, Type: "bool", Label: "LLM：要求与解析的集数一致", Description: "文件名解析出了集数而模型给出别的数字时只进审核", Default: true},
		{Key: KeyClefAccountID, Group: g, Type: "string", Label: "Clef：Cloudflare Account ID", Default: ""},
		{Key: KeyClefToken, Group: g, Type: "secret", Label: "Clef：API Token", Description: "需要 Workers AI 权限", Default: ""},
		{Key: KeyClefModel, Group: g, Type: "enum", Label: "Clef：模型", Options: []string{"clef-flash", "clef"}, Description: "clef-flash（9B，$0.038 / 百万输入 token）或 clef（27B，$0.24 / 百万）", Default: "clef-flash"},
		{Key: KeyClefBaseURL, Group: g, Type: "string", Label: "Clef：API 地址", Description: "默认 Cloudflare API；可指向本地模拟服务测试", Default: "https://api.cloudflare.com/client/v4"},
		{Key: KeyClefMetric, Group: g, Type: "enum", Label: "Clef：判定指标", Options: []string{"prob", "confidence"}, Description: "prob（推荐，已校准）= 概率：条目取「是否正是该条目」是/否问题的「是」概率，类型与集数取所选选项的概率；confidence = Clef 返回的 confidence（偏低，不推荐）。取该文件各问题的最小值", Default: "prob"},
		{Key: KeyClefApply, Group: g, Type: "number", Label: "Clef：直接应用阈值", Description: "指标 ≥ 此值且概率差 ≥ 下方「最小概率差」时直接写入映射（来源 clef）；早已完结的条目（完结超过 1 年，或无完结日期且开播超过 2 年）一律进入审核", Default: 0.8},
		{Key: KeyClefMargin, Group: g, Type: "number", Label: "Clef：最小概率差", Description: "第一与第二选项的概率差（取各问题最小值）", Default: 0.3},
		{Key: KeyClefReview, Group: g, Type: "number", Label: "Clef：人工审核阈值", Description: "介于此值与直接应用之间的进入审核队列，低于此值不映射", Default: 0.5},
		{Key: KeyClefRate, Group: g, Type: "number", Label: "Clef：每分钟请求数", Default: 60},
		{Key: KeyClefDailyCap, Group: g, Type: "number", Label: "Clef：每日请求上限", Description: "含后台测试；按 UTC 日期计", Default: 2000},
	}
}

var defaultTrackers = []string{
	"wss://tracker.projectk.org",
	"wss://tracker.dnlab.net:16443",
	"wss://tracker.openwebtorrent.com",
	"wss://tracker.btorrent.xyz",
}

// defaultBTTrackers: public UDP / HTTP trackers (checked reachable and scrape-capable,
// 2026-10) given to qB and used for swarm statistics.
var defaultBTTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://open.stealth.si:80/announce",
	"udp://exodus.desync.com:6969/announce",
	"http://nyaa.tracker.wf:7777/announce",
	"udp://explodie.org:6969/announce",
}

func validateRankRules(raw json.RawMessage) (json.RawMessage, error) {
	r, err := rank.Parse(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

// trackerList validates a tracker list setting: at most 50 URLs of the given schemes.
func trackerList(schemes ...string) func(json.RawMessage) (json.RawMessage, error) {
	return func(raw json.RawMessage) (json.RawMessage, error) {
		var l []string
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("需要字符串数组")
		}
		if len(l) > 50 {
			return nil, fmt.Errorf("最多 50 个 tracker")
		}
		for _, t := range l {
			u, err := url.Parse(t)
			if err != nil || u.Host == "" || u.Hostname() == "" || len(t) > 300 || !slices.Contains(schemes, strings.ToLower(u.Scheme)) {
				return nil, fmt.Errorf("无效的 tracker：%s（需要 %s）", t, strings.Join(schemes, " / "))
			}
			if strings.ToLower(u.Scheme) == "udp" && u.Port() == "" {
				return nil, fmt.Errorf("udp tracker 需要端口：%s", t)
			}
		}
		return json.Marshal(l)
	}
}

// SettingsSchema lists every setting; order is the admin form order.
func SettingsSchema() []SettingSpec {
	return append([]SettingSpec{
		{Key: KeySiteName, Group: "站点", Type: "string", Label: "站点名称", Default: "Project Kawaii", Public: true},
		{Key: KeySiteBaseURL, Group: "站点", Type: "string", Label: "站点地址", Description: "用于生成邀请链接等，例如 https://www.projectk.org", Default: "https://www.projectk.org"},
		{Key: KeyAnnouncement, Group: "站点", Type: "markdown", Label: "首页公告", Description: "Markdown，留空则不显示", Default: "", Public: true},

		{Key: KeyRegistration, Group: "用户", Type: "enum", Label: "注册方式", Options: []string{"open", "invite", "closed"}, Description: "open 开放注册；invite 需要邀请码；closed 关闭注册", Default: "invite", Public: true},
		{Key: KeySessionDays, Group: "用户", Type: "number", Label: "登录有效期（天）", Default: 30},
		{Key: KeyTurnstileSiteKey, Group: "用户", Type: "string", Label: "Turnstile Site Key", Description: "Cloudflare Turnstile，留空则不启用人机验证", Default: "", Public: true},
		{Key: KeyTurnstileSecret, Group: "用户", Type: "secret", Label: "Turnstile Secret", Default: ""},

		{Key: KeyTrackers, Group: "Tracker", Type: "string_list", Label: "WebTorrent Tracker", Description: "浏览器与 qB 用于互相发现的 wss tracker（公开，下发给浏览器）；也用于统计 WebTorrent 做种人数", Default: defaultTrackers, Public: true, Validate: trackerList("wss", "ws")},
		{Key: KeyBTTrackers, Group: "Tracker", Type: "string_list", Label: "BT Tracker", Description: "udp:// / http(s):// tracker：推送给 qB（与种子自带的 tracker、wss tracker 合并去重，最多 30 个），并用于统计 BT 做种人数；不再依赖 qB 的「自动添加 tracker」", Default: defaultBTTrackers, Validate: trackerList("udp", "http", "https")},
		{Key: KeySwarmEnabled, Group: "Tracker", Type: "bool", Label: "统计做种人数", Description: "按需向 tracker 查询（scrape）做种 / 下载人数：单集、条目、搜索、种子状态接口返回的种子若没有数据或超过 30 分钟，就在当时批量查询这些种子（最多等待约 1.5 秒，其余在后台完成）。不做全库定时查询。关闭后只显示已有数据（含 Nyaa RSS 的初始值）", Default: true},
		{Key: KeyRankRules, Group: "播放", Type: "json", Label: "推荐规则", Description: "单集页资源排序与自动播放（后台「推荐规则」页编辑）", Default: rank.Default(), Public: true, Hidden: true, Validate: validateRankRules},
		{Key: KeyICEServers, Group: "播放", Type: "string_list", Label: "ICE 服务器", Description: "stun:/turn: URL", Default: []string{"stun:stun.l.google.com:19302", "stun:stun.miwifi.com:3478"}, Public: true},
		{Key: KeyMaxAutoSize, Group: "播放", Type: "number", Label: "时间线自动选种大小上限（MB）", Default: 2048, Public: true},
		{Key: KeyMaxPushSize, Group: "播放", Type: "number", Label: "可播放种子大小上限（MB）", Description: "超过此大小的种子不会推送给 qB；用户自填磁力在获取元数据后超限会被移除", Default: 8192},

		{Key: KeyDefaultAction, Group: "审核", Type: "enum", Label: "默认权限", Options: []string{"allow", "login", "vip"}, Description: "没有任何规则命中时的权限要求", Default: "allow"},
		{Key: KeyAutoReviewUnknown, Group: "审核", Type: "bool", Label: "用户自填磁力进入审核队列", Description: "记录用户自行提交的种子，供管理员事后审核", Default: true},

		{Key: KeyUpstreamProxy, Group: "上游", Type: "string", Label: "上游代理", Description: "访问 bangumi.moe / bgm.tv 用的代理，如 socks5://127.0.0.1:1080，留空直连", Default: ""},
		{Key: KeyMoeBase, Group: "上游", Type: "string", Label: "bangumi.moe 地址", Default: "https://bangumi.moe"},
		{Key: KeyBangumiDataURL, Group: "上游", Type: "string", Label: "bangumi-data 地址", Default: "https://cdn.jsdelivr.net/npm/bangumi-data@0.3/dist/data.json"},
		{Key: KeyBgmUserAgent, Group: "上游", Type: "string", Label: "bgm.tv User-Agent", Default: "darknightlab/onlinetorrent (https://www.projectk.org)"},

		{Key: KeyTMDBKey, Group: "上游", Type: "secret", Label: "TMDB API Key", Description: "可选；本地搜不到番剧时也查询 TMDB（v3 API Key 或 v4 读取令牌），留空只查 bgm.tv", Default: ""},

		{Key: KeyBgmArchiveEnabled, Group: "抓取", Type: "bool", Label: "同步 bangumi 数据", Description: "每 6 小时检查 bangumi Archive（bgm.tv 官方每周数据导出），有新版本时导入全部动画条目、章节与条目关联；关闭后「立即同步」仍可用", Default: true},
		{Key: KeyCrawlEnabled, Group: "抓取", Type: "bool", Label: "启用后台抓取", Description: "总开关：按「抓取源」定时抓取 RSS / bangumi.moe 并入库；关闭后「立即抓取」仍可用", Default: true},
		{Key: KeyCrawlWorkers, Group: "抓取", Type: "number", Label: "抓取并发数", Description: "同时抓取的源数量（同一站点始终串行）", Default: 2},

		{Key: KeyCleanupEnabled, Group: "qBittorrent", Type: "bool", Label: "自动清理做种完成的种子", Description: "删除本站分类下已达到分享限制而停止的种子（含文件）", Default: true},
		{Key: KeyQBAddTimeoutSec, Group: "qBittorrent", Type: "number", Label: "推送超时（秒）", Default: 8},

		{Key: KeyWebseedEnabled, Group: "WebSeed", Type: "bool", Label: "启用 HTTP WebSeed 加速", Description: "qB 下载完成的文件可经本站以 HTTP 方式补充传输（经 Cloudflare 隧道，消耗服务器上行流量）", Default: true},
	}, clefSettings()...)
}

func specFor(key string) (SettingSpec, bool) {
	for _, s := range SettingsSchema() {
		if s.Key == key {
			return s, true
		}
	}
	return SettingSpec{}, false
}

// settingsCache is an in-memory snapshot of all settings, refreshed after writes.
type settingsCache struct {
	mu   sync.RWMutex
	vals map[string]json.RawMessage
}

func (s *Store) seedSettings(ctx context.Context) error {
	for _, spec := range SettingsSchema() {
		b, _ := json.Marshal(spec.Default)
		if _, err := s.W.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING`, spec.Key, string(b)); err != nil {
			return err
		}
	}
	// recommendation rules still equal to an earlier built-in default (never edited by an
	// admin): replace with the current default
	var cur string
	if err := s.W.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, KeyRankRules).Scan(&cur); err == nil && rank.IsLegacyDefault([]byte(cur)) {
		if _, err := s.W.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = ?`, string(rank.DefaultJSON()), KeyRankRules); err != nil {
			return err
		}
	}
	return s.reloadSettings(ctx)
}

func (s *Store) reloadSettings(ctx context.Context) error {
	rows, err := s.W.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		m[k] = json.RawMessage(v)
	}
	s.settings.mu.Lock()
	s.settings.vals = m
	s.settings.mu.Unlock()
	return rows.Err()
}

func (s *Store) rawSetting(key string) json.RawMessage {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.vals[key]
}

// Str returns a string setting.
func (s *Store) Str(key string) string {
	var v string
	_ = json.Unmarshal(s.rawSetting(key), &v)
	return v
}

// Num returns a numeric setting.
func (s *Store) Num(key string) float64 {
	var v float64
	_ = json.Unmarshal(s.rawSetting(key), &v)
	return v
}

// Bool returns a bool setting.
func (s *Store) Bool(key string) bool {
	var v bool
	_ = json.Unmarshal(s.rawSetting(key), &v)
	return v
}

// List returns a string-list setting.
func (s *Store) List(key string) []string {
	var v []string
	_ = json.Unmarshal(s.rawSetting(key), &v)
	return v
}

// SettingItem is what the admin API returns.
type SettingItem struct {
	SettingSpec
	Value any  `json:"value"`
	IsSet bool `json:"is_set,omitempty"`
}

// SettingItems returns the admin view (secrets masked).
func (s *Store) SettingItems() []SettingItem {
	var out []SettingItem
	for _, spec := range SettingsSchema() {
		if spec.Hidden {
			continue
		}
		var v any
		_ = json.Unmarshal(s.rawSetting(spec.Key), &v)
		it := SettingItem{SettingSpec: spec, Value: v}
		if spec.Type == "secret" {
			str, _ := v.(string)
			it.IsSet = str != ""
			it.Value = ""
		}
		if spec.Type == "string_list" && v == nil {
			it.Value = []string{}
		}
		out = append(out, it)
	}
	return out
}

// UpdateSettings validates and writes values. Empty secret = unchanged.
func (s *Store) UpdateSettings(ctx context.Context, values map[string]json.RawMessage) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, raw := range values {
		spec, ok := specFor(k)
		if !ok || spec.Hidden {
			return fmt.Errorf("unknown setting %q", k)
		}
		norm, skip, err := normalizeSetting(spec, raw)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		if skip {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, norm); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reloadSettings(ctx)
}

// Raw returns the stored JSON of a setting.
func (s *Store) Raw(key string) json.RawMessage { return s.rawSetting(key) }

// SetJSON validates (spec.Validate) and stores a hidden json setting, returning the
// stored value.
func (s *Store) SetJSON(ctx context.Context, key string, raw json.RawMessage) (json.RawMessage, error) {
	spec, ok := specFor(key)
	if !ok || spec.Type != "json" {
		return nil, fmt.Errorf("unknown setting %q", key)
	}
	norm, _, err := normalizeSetting(spec, raw)
	if err != nil {
		return nil, err
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, norm); err != nil {
		return nil, err
	}
	return json.RawMessage(norm), s.reloadSettings(ctx)
}

func normalizeSetting(spec SettingSpec, raw json.RawMessage) (string, bool, error) {
	switch spec.Type {
	case "json":
		if spec.Validate != nil {
			v, err := spec.Validate(raw)
			if err != nil {
				return "", false, err
			}
			return string(v), false, nil
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil || v == nil {
			return "", false, fmt.Errorf("需要 JSON 对象")
		}
		b, _ := json.Marshal(v)
		return string(b), false, nil
	case "string", "text", "markdown", "secret", "enum":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", false, fmt.Errorf("需要字符串")
		}
		if spec.Type == "secret" && v == "" {
			return "", true, nil
		}
		if spec.Type == "enum" && !slices.Contains(spec.Options, v) {
			return "", false, fmt.Errorf("取值必须是 %v", spec.Options)
		}
		b, _ := json.Marshal(v)
		return string(b), false, nil
	case "number":
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", false, fmt.Errorf("需要数字")
		}
		b, _ := json.Marshal(v)
		return string(b), false, nil
	case "bool":
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", false, fmt.Errorf("需要布尔值")
		}
		b, _ := json.Marshal(v)
		return string(b), false, nil
	case "string_list":
		var v []string
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", false, fmt.Errorf("需要字符串数组")
		}
		clean := make([]string, 0, len(v))
		for _, x := range v {
			if x = strings.TrimSpace(x); x != "" && !slices.Contains(clean, x) {
				clean = append(clean, x)
			}
		}
		if spec.Validate != nil {
			b, _ := json.Marshal(clean)
			out, err := spec.Validate(b)
			if err != nil {
				return "", false, err
			}
			return string(out), false, nil
		}
		b, _ := json.Marshal(clean)
		return string(b), false, nil
	}
	return "", false, fmt.Errorf("unsupported type")
}

var _ = sql.ErrNoRows
