package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
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
	KeyICEServers        = "webtorrent.ice_servers"
	KeyMaxAutoSize       = "play.max_auto_size_mb"
	KeyMaxPushSize       = "play.max_push_size_mb"
	KeyDefaultAction     = "policy.default_action"
	KeyUserMagnetAction  = "policy.user_magnet_action"
	KeyAutoReviewUnknown = "policy.review_user_magnets"
	KeyUpstreamProxy     = "upstream.proxy"
	KeyMoeBase           = "upstream.bangumi_moe"
	KeyBangumiDataURL    = "upstream.bangumi_data_url"
	KeyBgmUserAgent      = "upstream.bgm_user_agent"
	KeyCleanupEnabled    = "qb.cleanup_enabled"
	KeyQBAddTimeoutSec   = "qb.add_timeout_sec"
	KeyWebseedEnabled    = "webseed.enabled"
	KeyWebseedDelaySec   = "webseed.delay_sec"
)

var defaultTrackers = []string{
	"wss://tracker.projectk.org",
	"wss://tracker.dnlab.net:16443",
	"wss://tracker.openwebtorrent.com",
	"wss://tracker.btorrent.xyz",
}

// SettingsSchema lists every setting; order is the admin form order.
func SettingsSchema() []SettingSpec {
	return []SettingSpec{
		{Key: KeySiteName, Group: "站点", Type: "string", Label: "站点名称", Default: "Project Kawaii", Public: true},
		{Key: KeySiteBaseURL, Group: "站点", Type: "string", Label: "站点地址", Description: "用于生成邀请链接等，例如 https://www.projectk.org", Default: "https://www.projectk.org"},
		{Key: KeyAnnouncement, Group: "站点", Type: "markdown", Label: "首页公告", Description: "Markdown，留空则不显示", Default: "", Public: true},

		{Key: KeyRegistration, Group: "用户", Type: "enum", Label: "注册方式", Options: []string{"open", "invite", "closed"}, Description: "open 开放注册；invite 需要邀请码；closed 关闭注册", Default: "invite", Public: true},
		{Key: KeySessionDays, Group: "用户", Type: "number", Label: "登录有效期（天）", Default: 30},
		{Key: KeyTurnstileSiteKey, Group: "用户", Type: "string", Label: "Turnstile Site Key", Description: "Cloudflare Turnstile，留空则不启用人机验证", Default: "", Public: true},
		{Key: KeyTurnstileSecret, Group: "用户", Type: "secret", Label: "Turnstile Secret", Default: ""},

		{Key: KeyTrackers, Group: "播放", Type: "string_list", Label: "WebTorrent Tracker", Description: "浏览器与 qB 用于互相发现的 wss tracker", Default: defaultTrackers, Public: true},
		{Key: KeyICEServers, Group: "播放", Type: "string_list", Label: "ICE 服务器", Description: "stun:/turn: URL", Default: []string{"stun:stun.l.google.com:19302", "stun:stun.miwifi.com:3478"}, Public: true},
		{Key: KeyMaxAutoSize, Group: "播放", Type: "number", Label: "时间线自动选种大小上限（MB）", Default: 2048, Public: true},
		{Key: KeyMaxPushSize, Group: "播放", Type: "number", Label: "可播放种子大小上限（MB）", Description: "超过此大小的种子不会推送给 qB；用户自填磁力在获取元数据后超限会被移除", Default: 8192},

		{Key: KeyDefaultAction, Group: "审核", Type: "enum", Label: "默认权限", Options: []string{"allow", "login", "vip"}, Description: "没有任何规则命中时的权限要求", Default: "allow"},
		{Key: KeyUserMagnetAction, Group: "审核", Type: "enum", Label: "用户自填磁力最低权限", Options: []string{"allow", "login", "vip"}, Description: "非 bangumi.moe 来源的种子至少需要的权限", Default: "login"},
		{Key: KeyAutoReviewUnknown, Group: "审核", Type: "bool", Label: "用户自填磁力进入审核队列", Description: "记录用户自行提交的种子，供管理员事后审核", Default: true},

		{Key: KeyUpstreamProxy, Group: "上游", Type: "string", Label: "上游代理", Description: "访问 bangumi.moe / bgm.tv 用的代理，如 socks5://127.0.0.1:1080，留空直连", Default: ""},
		{Key: KeyMoeBase, Group: "上游", Type: "string", Label: "bangumi.moe 地址", Default: "https://bangumi.moe"},
		{Key: KeyBangumiDataURL, Group: "上游", Type: "string", Label: "bangumi-data 地址", Default: "https://cdn.jsdelivr.net/npm/bangumi-data@0.3/dist/data.json"},
		{Key: KeyBgmUserAgent, Group: "上游", Type: "string", Label: "bgm.tv User-Agent", Default: "darknightlab/onlinetorrent (https://www.projectk.org)"},

		{Key: KeyCleanupEnabled, Group: "qBittorrent", Type: "bool", Label: "自动清理做种完成的种子", Description: "删除本站分类下已达到分享限制而停止的种子（含文件）", Default: true},
		{Key: KeyQBAddTimeoutSec, Group: "qBittorrent", Type: "number", Label: "推送超时（秒）", Default: 8},

		{Key: KeyWebseedEnabled, Group: "WebSeed", Type: "bool", Label: "启用 HTTP WebSeed 加速", Description: "qB 下载完成的文件可经本站以 HTTP 方式补充传输（经 Cloudflare 隧道，消耗服务器上行流量）", Default: true},
		{Key: KeyWebseedDelaySec, Group: "WebSeed", Type: "number", Label: "WebSeed 延迟（秒）", Description: "开始播放后等待多少秒再启用 WebSeed，优先使用 P2P", Default: 8},
	}
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
		if !ok {
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

func normalizeSetting(spec SettingSpec, raw json.RawMessage) (string, bool, error) {
	switch spec.Type {
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
			if x != "" {
				clean = append(clean, x)
			}
		}
		b, _ := json.Marshal(clean)
		return string(b), false, nil
	}
	return "", false, fmt.Errorf("unsupported type")
}

var _ = sql.ErrNoRows
