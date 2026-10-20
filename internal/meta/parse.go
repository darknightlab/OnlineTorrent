package meta

// Release / file-name parser.
//
// The parser works by token elimination, modelled on Anitomy
// (https://github.com/erengy/anitomy, C++, MPL-2.0) and its Go port anitogo
// (https://github.com/nssteinbrenner/anitogo, MPL-2.0). Parts of the keyword tables below
// (codecs, sources, languages, release info, episode prefixes, "ambiguous" type words) are
// derived from Anitomy's keyword list; the code is our own.
//
//  1. tokenise: brackets ([] 【】 () （） 「」 『』 {}) open groups; spaces _ . ; , & + / ! :
//     ★ · ・ and standalone dashes delimit tokens. Dots and dashes inside a chunk only split
//     it when the chunk is not a known token itself (WEB-DL, H.264, 12.5, 1999-04-24, 01-12).
//     Mixed CJK/latin chunks are split by script after CJK patterns (第12话, 第2季, 10月,
//     ページ173, ノンテロップ, 简繁内封 …) have been taken out.
//  2. identify: every token that has a known meaning is eliminated — resolution, video/audio
//     codec, bit depth, source/platform (BD, WEB-DL, Baha, CR, B-Global …), language and
//     subtitle tags (CHS/CHT/GB/BIG5/简繁/内封 …), edition (…版), CRC32, version (v2), date,
//     year, month, season (S2, Season 2, 2nd Season, 第二季, 2기), batch ranges (01-12), the
//     release group (first bracket), title numbers (No. 8, Vol.1, Part 2), and type keywords
//     (SP/OVA/OAD/Special, NCOP/NCED/ノンテロップ, OP/ED, PV/CM/Trailer/Teaser, Menu, Preview/
//     予告, 特典, Sample …).
//  3. decide: the episode number is chosen among what is left. Explicit forms (S01E02, EP02,
//     第2话) beat " - 02", which beats an isolated bracket "[02]", which beats a bare number.
//     Bare numbers glued to a title word only count when they look like an episode ("Show
//     03", not "Mob Psycho 100"). For specials the number written right after the keyword
//     wins ("SP02", "Special S1 01"); otherwise the single remaining number is used. Several
//     different candidates of the best kind, or none at all, give Episode=nil (never a
//     default number).
//
// ClassifyFile adds folder names (SPs/, NCOP/, Extras/ …) and the torrent title; ClassifyFiles
// cross-checks all files of a torrent (see there).

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var videoExts = map[string]bool{".mkv": true, ".mp4": true, ".webm": true, ".mov": true, ".m4v": true, ".avi": true, ".ts": true, ".m2ts": true, ".flv": true, ".ogv": true, ".3gp": true}

// IsVideo reports whether a path looks like a video file.
func IsVideo(p string) bool { return videoExts[strings.ToLower(path.Ext(p))] }

// ParsedName is the result of release-name parsing.
type ParsedName struct {
	Episode    *float64
	Kind       string // episode|sp|op|ed|pv|cm|extra|other
	Confidence float64
	// Season is the TV season marker of the name (S2, Season 2, 2nd Season, 第二季, 第2期,
	// S02E05, a Ⅱ/II suffix …); 0 = none. Markers that number a special ("Special S2") and
	// names holding several different seasons ("S1+S2") do not count. ClassifyFile(s) fall
	// back to the innermost folder carrying a season.
	Season float64
	// Sure: the number comes from an unambiguous marker — an explicit episode form (S01E02,
	// EP02, 第2话), " - 02", an isolated bracket "[02]", or a special's own number ("SP02",
	// "NCOP 2") — and was not inferred (bare numbers, the smaller of a bracket pair, the
	// torrent title of a single video, the only unnumbered special of its kind → 1, …).
	// Rule-based mapping only maps sure numbers; the rest goes to the decision model.
	Sure bool
}

// ---------------------------------------------------------------------------------------
// tokens

type tcat uint8

const (
	cWord    tcat = iota // unknown text: title, episode title
	cNum                 // bare number: episode candidate
	cDash                // standalone " - " separator
	cIdent               // identified metadata, eliminated
	cYear                // 1900–2100
	cSeason              // S2, Season 2, 第二季
	cRange               // batch range 01-12, EP21-EP26, 01~14話
	cEpisode             // explicit episode: S01E02, EP02, 第2话, #2, ページ2
	cType                // type keyword (typ holds the kind)
	cNC                  // non-credit marker qualifying a following OP/ED (ノンテロップ, 无字)
	cVersion             // v2
)

type token struct {
	text   string // width-normalised text
	cat    tcat
	group  int  // bracket group id, 0 = outside brackets
	glued  bool // no delimiter between this token and the previous one
	num    float64
	hasNum bool   // num is set (cNum, cEpisode, cSeason, cYear; cType: own number)
	typ    string // cType: sp|op|ed|oped|pv|cm|extra|sample|nc
	ord    bool   // cWord: ordinal ("2nd")
	trust  bool   // cType: keyword is meant as a type (see trustTypes)
	used   bool   // cNum consumed as a type keyword's own number
	pair   bool   // cNum: the smaller of two numbers that alone fill a bracket ("[03_51]")
}

var bracketPairs = map[rune]rune{'[': ']', '【': '】', '(': ')', '「': '」', '『': '』', '{': '}', '〔': '〕', '〖': '〗'}

func isCloser(r rune) bool {
	switch r {
	case ']', '】', ')', '」', '』', '}', '〕', '〗':
		return true
	}
	return false
}

func isDelim(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '_', ';', ',', '|', '+', '&', '/', '\\', ':', '!', '?', '★', '☆', '♪', '♥', '·', '・', '"', '\'', '`', '*', '、', '。', '…', '<', '>', '=':
		return true
	}
	return false
}

func isDashText(s string) bool {
	switch s {
	case "-", "–", "—", "−", "~", "--":
		return true
	}
	return false
}

// normWidth maps full-width ASCII (＆ ０-９ Ａ-Ｚ （ ） ：…) to ASCII and the ideographic space
// to a space.
func normWidth(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == 0x3000:
			return ' '
		}
		return r
	}, s)
}

type rawChunk struct {
	text  string
	group int
}

// splitChunks splits on brackets and delimiters. groups are numbered from 1 in order of
// their opening bracket; nested brackets get their own group.
func splitChunks(s string) (chunks []rawChunk, firstGroup int) {
	type open struct {
		close rune
		id    int
	}
	var stack []open
	nextID := 0
	var cur strings.Builder
	group := func() int {
		if len(stack) == 0 {
			return 0
		}
		return stack[len(stack)-1].id
	}
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, rawChunk{cur.String(), group()})
			cur.Reset()
		}
	}
	for i, r := range s {
		if cl, ok := bracketPairs[r]; ok {
			flush()
			nextID++
			if i == 0 {
				firstGroup = nextID
			}
			stack = append(stack, open{cl, nextID})
			continue
		}
		if isCloser(r) {
			flush()
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].close == r {
					stack = stack[:j]
					break
				}
			}
			continue
		}
		if isDelim(r) {
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return chunks, firstGroup
}

// reProtect: multi-part tokens that must not be split on '.' or '-' inside a chunk.
var reProtect = regexp.MustCompile(`(?i)(?:19|20)\d\d[-.](?:0?[1-9]|1[0-2])[-.](?:0?[1-9]|[12]\d|3[01])\b|S\d{1,2}E\d{1,4}(?:-E?\d{1,4})?|EP?\d{1,4}-(?:EP?)?\d{1,4}\b|[HX]\.26[45]|(?:AAC|DDP|DD|EAC3|AC3|DTS|OPUS|FLAC|TRUEHD)\d\.\d|DTS-HD(?:\.?MA)?|E-AC-?3|WEB-?DL|WEB-?RIP|BLU-?RAY|DVD-?RIP|TV-?RIP|B-GLOBAL|AT-X|U-NEXT|(?:HEVC|AVC|X26[45])-\d{1,2}-?BITS?|\d{1,2}-BITS?|MULTI-(?:SUBS?|AUDIO)|DUAL-AUDIO|\b\d{1,4}\.\d(?:v\d)?\b|\b\d{1,4}[-~]\d{1,4}\b`)

// CJK patterns, applied to chunk parts containing non-ASCII text before script splitting.
type cjkPat struct {
	re *regexp.Regexp
	fn func(m []string) token
}

var cjkNumerals = map[rune]float64{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '十': 10,
	'壱': 1, '弐': 2, '参': 3} // old forms (第弐幕)

func cjkNum(s string) (float64, bool) {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v, true
	}
	// 一 … 九, 十, 十一 … 十九, 二十 …
	var total, cur float64
	for _, r := range s {
		v, ok := cjkNumerals[r]
		if !ok {
			return 0, false
		}
		if v == 10 {
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
		} else {
			cur = v
		}
	}
	return total + cur, total+cur > 0
}

func numTok(cat tcat, s string) token {
	t := token{text: s, cat: cat}
	if v, ok := cjkNum(s); ok {
		t.num, t.hasNum = v, true
	}
	return t
}

var cjkPats = []cjkPat{
	{regexp.MustCompile(`第\s*\d{1,4}\s*[-~]\s*\d{1,4}\s*[季期部话話集回]`), func(m []string) token { return token{text: m[0], cat: cRange} }},
	{regexp.MustCompile(`\d{1,4}\s*[-~]\s*\d{1,4}\s*[话話集]`), func(m []string) token { return token{text: m[0], cat: cRange} }},
	{regexp.MustCompile(`第\s*(\d{1,4}(?:\.\d)?)\s*[话話集回]`), func(m []string) token { t := numTok(cEpisode, m[1]); t.text = m[0]; return t }},
	{regexp.MustCompile(`(\d{1,4}(?:\.\d)?)\s*[话話集]`), func(m []string) token { t := numTok(cEpisode, m[1]); t.text = m[0]; return t }},
	// 第2季 / 第二期 / 第三部; 第弐幕 (an "act" = part / season: 東京魔人學園剣風帖 龖 第弐幕)
	{regexp.MustCompile(`第\s*([0-9一二三四五六七八九十壱弐参]{1,3})\s*[季期部幕]`), func(m []string) token { t := numTok(cSeason, m[1]); t.text = m[0]; return t }},
	{regexp.MustCompile(`(\d{1,2})\s*[기期]`), func(m []string) token { t := numTok(cSeason, m[1]); t.text = m[0]; return t }},
	{regexp.MustCompile(`\d{1,2}\s*月`), func(m []string) token { return token{text: m[0], cat: cIdent} }},
	{regexp.MustCompile(`((?:19|20)\d\d)\s*年`), func(m []string) token { t := numTok(cYear, m[1]); t.text = m[0]; return t }},
	{regexp.MustCompile(`ページ\s*(\d{1,4})`), func(m []string) token { t := numTok(cEpisode, m[1]); t.text = m[0]; return t }},
	{regexp.MustCompile(`ノンテロップ|ノンクレジット|ノンクレ|无字幕|無字幕|无字|無字|去字幕`), func(m []string) token { return token{text: m[0], cat: cNC, typ: "nc"} }},
	{regexp.MustCompile(`オープニング|片头曲?|片頭曲?`), func(m []string) token { return cjkType(m[0], "op") }},
	{regexp.MustCompile(`エンディング|片尾曲?`), func(m []string) token { return cjkType(m[0], "ed") }},
	{regexp.MustCompile(`特報|特报|宣传片|宣傳片|プロモーション`), func(m []string) token { return cjkType(m[0], "pv") }},
	{regexp.MustCompile(`スポット|广告|廣告`), func(m []string) token { return cjkType(m[0], "cm") }},
	{regexp.MustCompile(`次回予告|予告|预告|預告|映像特典|特典映像|特典|花絮|ダイジェスト|メニュー|菜单|番宣|メイキング|インタビュー`), func(m []string) token { return cjkType(m[0], "extra") }},
	{regexp.MustCompile(`特别篇|特別篇|番外篇|番外編|番外|特番`), func(m []string) token { return cjkType(m[0], "sp") }},
	{regexp.MustCompile(`剧场版|劇場版`), func(m []string) token { return token{text: m[0], cat: cIdent} }},
}

func cjkType(s, typ string) token { return token{text: s, cat: cType, typ: typ, trust: true} }

// CJK words made only of language/subtitle characters (简繁内封, 简日双语, 内封简繁中字 …).
const langChars = "简繁簡體体内內封嵌外挂掛字幕中日双雙语語文"
const langMarks = "简繁簡內内封嵌挂掛幕双雙"

var cjkIdentWords = map[string]bool{"国漫": true, "國漫": true, "新番": true, "番剧": true, "番劇": true, "合集": true, "全集": true, "完结": true, "完結": true, "完": true, "无修": true, "無修": true, "无修正": true, "無修正": true, "修正": true, "生肉": true, "熟肉": true}

func classifyCJK(s string) token {
	if cjkIdentWords[s] || (strings.HasSuffix(s, "版") && utf8.RuneCountInString(s) >= 2) {
		return token{text: s, cat: cIdent}
	}
	onlyLang, mark := true, false
	for _, r := range s {
		if !strings.ContainsRune(langChars, r) {
			onlyLang = false
			break
		}
		if strings.ContainsRune(langMarks, r) {
			mark = true
		}
	}
	if onlyLang && mark {
		return token{text: s, cat: cIdent}
	}
	return token{text: s, cat: cWord}
}

// identWords: tokens (upper case) that carry release metadata.
var identWords = setOf(
	// source
	"BD", "BDRIP", "BDREMUX", "BDMV", "BLURAY", "BLU-RAY", "DVD", "DVD5", "DVD9", "DVDRIP", "DVD-RIP", "DVDISO", "R2J", "R2DVD", "R2JDVD",
	"HDTV", "HDTVRIP", "TVRIP", "TV-RIP", "WEB", "WEBRIP", "WEB-RIP", "WEB-DL", "WEBDL", "WEBCAST", "REMUX", "UHD", "ISO", "LASERDISC", "LD", "VHS", "VHSRIP",
	// streaming platforms / broadcasters
	"ABEMA", "BAHA", "CR", "B-GLOBAL", "BGLOBAL", "BILIBILI", "NF", "NETFLIX", "AMZN", "AMAZON", "DSNP", "HIDIVE", "IQIYI", "VIUTV", "VIU",
	"TVER", "LFTLNET", "AT-X", "ATX", "BS11", "U-NEXT", "UNEXT", "ADN", "FOD", "DMM", "LEMINO", "YOUTUBE", "ANIPLUS", "KKTV", "LINETV", "MYVIDEO", "CATCHPLAY", "TVB", "MBS", "TBS", "TOKYOMX", "MX",
	// video
	"HEVC", "HEVC2", "AVC", "AV1", "VP9", "X264", "X265", "H264", "H265", "H.264", "H.265", "X.264", "X.265", "XVID", "DIVX", "HI10", "HI10P", "HI444", "HI444P", "HI444PP", "MA10P",
	"HDR", "HDR10", "HDR10+", "SDR", "DV", "DOVI", "4K", "2K", "8K", "FHD", "HD", "SD", "HQ", "LQ", "VFR", "CFR", "60FPS", "30FPS", "24FPS", "23.976FPS", "VERYSLOW", "YUV420P10",
	"8BIT", "8-BIT", "10BIT", "10BITS", "10-BIT", "10-BITS", "12BIT",
	// audio
	"AAC", "AACX2", "AACX3", "AACX4", "AC3", "EAC3", "E-AC-3", "E-AC3", "FLAC", "FLACX2", "FLACX3", "MP3", "OPUS", "DTS", "DTS-HD", "DTS-ES", "DTS-HDMA", "TRUEHD", "ATMOS", "DDP", "DD", "DD+",
	"PCM", "LPCM", "VORBIS", "OGG", "LOSSLESS", "DUAL", "DUALAUDIO", "DUAL-AUDIO", "MULTIAUDIO", "MULTI-AUDIO", "MA", "2CH", "2.0CH", "5.1CH", "7.1CH", "2.0", "5.1", "7.1", "2.1",
	// container
	"MKV", "MP4", "AVI", "M2TS", "RMVB", "WMV", "FLV", "WEBM",
	// subtitles / languages
	"ASS", "ASSX2", "SRT", "SRTX2", "PGS", "SSA", "SUP", "SUB", "SUBS", "SUBBED", "SOFTSUB", "SOFTSUBS", "HARDSUB", "HARDSUBS", "RAW", "RAWS", "DUB", "DUBBED", "DUBS",
	"MULTISUB", "MULTISUBS", "MULTI-SUB", "MULTI-SUBS", "MSUBS", "MULTI", "VOSTFR", "SUBFRENCH", "TRUEFRENCH", "FRENCH", "VF", "VOSTA", "ENGLISH", "JAPANESE", "CHINESE",
	"CHS", "CHT", "SC", "TC", "GB", "BIG5", "JPSC", "JPTC", "JPCN", "JP", "JPN", "JAP", "CN", "ZH", "ENG", "RUS", "YUE", "CHI", "CANTONESE", "MANDARIN",
	// release info
	"BATCH", "COMPLETE", "PATCH", "REPACK", "PROPER", "UNCENSORED", "UNCENCORED", "UNCUT", "CENSORED", "REMASTER", "REMASTERED", "WIDESCREEN", "WS", "NOLOGO",
	"TV", "MOVIE", "FILM", "GEKIJOUBAN", "ONA", "SEASON", "SAISON",
)

func setOf(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// type keywords (upper case) → kind. "oped" = a combined OP&ED video, "nc" = creditless marker.
var typeWords = map[string]string{
	"SP": "sp", "SPS": "sp", "SPECIAL": "sp", "SPECIALS": "sp", "OVA": "sp", "OVAS": "sp", "OAD": "sp", "OADS": "sp", "OAV": "sp",
	"NCOP": "op", "NCED": "ed", "OP": "op", "ED": "ed", "OPENING": "op", "ENDING": "ed", "NCOPED": "oped", "NC": "nc", "CREDITLESS": "nc",
	"PV": "pv", "PVS": "pv", "TRAILER": "pv", "TRAILERS": "pv", "TEASER": "pv", "TEASERS": "pv", "CM": "cm", "CMS": "cm",
	"PREVIEW": "extra", "PREVIEWS": "extra", "MENU": "extra", "MENUS": "extra", "MAKING": "extra", "INTERVIEW": "extra", "DIGEST": "extra",
	"BONUS": "extra", "EXTRA": "extra", "EXTRAS": "extra",
	"SAMPLE": "sample", "SAMPLES": "sample",
}

// type keywords that are never anything else, so they count anywhere in a name (the
// others — SP, OP, ED, Special, Preview … — also occur in titles: "takt op.Destiny").
var alwaysTypes = setOf("NCOP", "NCED", "NCOPED", "OVA", "OVAS", "OAD", "OADS", "OAV", "SAMPLE", "SAMPLES", "MAKING")

// words followed by a number that belongs to the title (No. 8, Vol.1, Part 2, Disc 4).
var titleNumPrefix = setOf("VOL", "VOLUME", "PART", "COUR", "DISC", "DISK", "CD", "NR", "PARTE", "MOVIE", "FILM", "GEKIJOUBAN")

// episode prefix words followed by a number (Episode 5, EP 05, # 3).
var epPrefix = setOf("EP", "EPS", "EPISODE", "EPISODES", "EPISODIO", "FOLGE", "CAPITULO", "#")

var (
	reNum       = regexp.MustCompile(`^(\d{1,4}(?:\.\d)?)(?:V\d)?(END|FIN|完)?$`)
	reDecimalCh = regexp.MustCompile(`^[257]\.[01]$`)
	reRes       = regexp.MustCompile(`^(?:AI)?\d{3,4}[PI]$|^\d{3,4}[X×]\d{3,4}[PI]?$`)
	reCRC       = regexp.MustCompile(`^[0-9A-F]{8}$`)
	reSxxExx    = regexp.MustCompile(`^S(\d{1,2})E(\d{1,4}(?:\.\d)?)(?:V\d)?$`)
	reSERange   = regexp.MustCompile(`^S\d{1,2}E\d{1,4}-E?\d{1,4}$`)
	reEpRange   = regexp.MustCompile(`^EP?(\d{1,4})-(?:EP?)?(\d{1,4})$`)
	reEpWord    = regexp.MustCompile(`^(?:EP|EPS|E|EPISODE|#)\.?(\d{1,4}(?:\.\d)?)(?:V\d)?$`)
	reRange     = regexp.MustCompile(`^(\d{1,4})[-~](\d{1,4})(?:END|FIN)?$`)
	reSeason    = regexp.MustCompile(`^S(\d{1,2})$`)
	reOrdinal   = regexp.MustCompile(`^(\d{1,2})(?:ST|ND|RD|TH)$`)
	reCount     = regexp.MustCompile(`^\d{1,3}S\d{1,4}E$`) // "5S14E": 5 seasons, 14 episodes
	reVersion   = regexp.MustCompile(`^V\d$`)
	reTitleNum  = regexp.MustCompile(`^(?:VOL|DISC|DISK|PART|CD)\.?\d{1,3}$`)
	reType      = regexp.MustCompile(`^([A-Z]+)(\d{1,3})?(?:V\d)?$`)
	reDate      = regexp.MustCompile(`^(?:19|20)\d\d[-./](?:0?[1-9]|1[0-2])[-./](?:0?[1-9]|[12]\d|3[01])$`)
	reCodecBits = regexp.MustCompile(`^(?:HEVC|AVC|X26[45]|H26[45])-?\d{1,2}-?BITS?$|^\d{1,2}-?BITS?$`)
	reAudioCh   = regexp.MustCompile(`^(?:AAC|DDP|DD|EAC3|AC3|DTS|OPUS|FLAC|TRUEHD|PCM|LPCM)(?:X\d)?\d\.\d(?:CH)?$|^\d\.\dCH$`)
	reSubCount  = regexp.MustCompile(`^(?:ASS|SRT|PGS|SSA|SUB|AAC|FLAC|AC3)X\d$`)
)

// classifyWord classifies one ASCII-ish piece.
func classifyWord(w string) token {
	t := token{text: w, cat: cWord}
	u := strings.ToUpper(w)
	if isDashText(u) {
		t.cat = cDash
		return t
	}
	if m := reNum.FindStringSubmatch(u); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		switch {
		case reDecimalCh.MatchString(u):
			t.cat = cIdent // 2.0 / 5.1 audio channels
		case !strings.Contains(m[1], ".") && len(m[1]) == 4 && v >= 1900 && v <= 2100:
			t.cat, t.num, t.hasNum = cYear, v, true
		default:
			t.cat, t.num, t.hasNum = cNum, v, true
		}
		return t
	}
	if len(u) >= 5 && strings.Trim(u, "0123456789") == "" {
		t.cat = cIdent // long digit runs: dates, ids
		return t
	}
	switch {
	case identWords[u], reRes.MatchString(u), reCodecBits.MatchString(u), reAudioCh.MatchString(u), reSubCount.MatchString(u),
		reDate.MatchString(u), reCount.MatchString(u), reTitleNum.MatchString(u):
		t.cat = cIdent
		return t
	case reCRC.MatchString(u) && strings.ContainsAny(u, "0123456789") && strings.ContainsAny(u, "ABCDEF"):
		t.cat = cIdent
		return t
	case reVersion.MatchString(u):
		t.cat = cVersion
		return t
	case reSERange.MatchString(u), reEpRange.MatchString(u):
		t.cat = cRange
		return t
	}
	if m := reSxxExx.FindStringSubmatch(u); m != nil {
		t.cat = cEpisode
		t.num, _ = strconv.ParseFloat(m[2], 64)
		t.hasNum = true
		return t
	}
	if m := reRange.FindStringSubmatch(u); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		if a < b {
			t.cat = cRange
		} else {
			t.cat = cIdent
		}
		return t
	}
	if m := reEpWord.FindStringSubmatch(u); m != nil {
		t.cat = cEpisode
		t.num, _ = strconv.ParseFloat(m[1], 64)
		t.hasNum = true
		return t
	}
	if m := reSeason.FindStringSubmatch(u); m != nil {
		t.cat = cSeason
		t.num, _ = strconv.ParseFloat(m[1], 64)
		t.hasNum = true
		return t
	}
	if m := reOrdinal.FindStringSubmatch(u); m != nil {
		t.ord = true
		t.num, _ = strconv.ParseFloat(m[1], 64)
		t.hasNum = true
		return t
	}
	if m := reType.FindStringSubmatch(u); m != nil {
		if typ, ok := typeWords[m[1]]; ok && (m[2] == "" || typ != "nc") {
			t.cat, t.typ = cType, typ
			t.trust = alwaysTypes[m[1]]
			if m[2] != "" {
				t.num, _ = strconv.ParseFloat(m[2], 64)
				t.hasNum, t.trust = true, true // SP01, NCOP1, CM02, Menu01
			}
			if typ == "nc" {
				t.cat = cNC
			}
			return t
		}
	}
	return t
}

func hasNonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return true
		}
	}
	return false
}

func isASCIIAlnum(r rune) bool {
	return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '#')
}

// splitCJK takes the CJK patterns out of s and splits the rest by script.
func splitCJK(s string) []token {
	var out []token
	for s != "" {
		best, bestPat := []int(nil), -1
		for i, p := range cjkPats {
			if loc := p.re.FindStringSubmatchIndex(s); loc != nil && (best == nil || loc[0] < best[0]) {
				best, bestPat = loc, i
			}
		}
		if best == nil {
			out = append(out, splitScript(s)...)
			break
		}
		if best[0] > 0 {
			out = append(out, splitScript(s[:best[0]])...)
		}
		var m []string
		for i := 0; i+1 < len(best); i += 2 {
			if best[i] < 0 {
				m = append(m, "")
			} else {
				m = append(m, s[best[i]:best[i+1]])
			}
		}
		out = append(out, cjkPats[bestPat].fn(m))
		s = s[best[1]:]
	}
	return out
}

func splitScript(s string) []token {
	var out []token
	start, ascii := 0, false
	emit := func(part string, isASCII bool) {
		part = strings.Trim(part, ".")
		if part == "" {
			return
		}
		if isASCII {
			out = append(out, classifyWord(part))
		} else {
			out = append(out, classifyCJK(part))
		}
	}
	for i, r := range s {
		a := isASCIIAlnum(r)
		if i == 0 {
			ascii = a
			continue
		}
		if a != ascii {
			emit(s[start:i], ascii)
			start, ascii = i, a
		}
	}
	emit(s[start:], ascii)
	return out
}

// splitChunk turns one delimiter-free chunk into classified tokens.
func splitChunk(c string) []token {
	if isDashText(c) {
		return []token{{text: c, cat: cDash}}
	}
	c = strings.Trim(c, ".-–—")
	if c == "" {
		return nil
	}
	if !hasNonASCII(c) {
		if t := classifyWord(c); t.cat != cWord || t.ord {
			return []token{t}
		}
	}
	var parts []token
	pos := 0
	splitGap := func(g string) {
		for _, p := range strings.FieldsFunc(g, func(r rune) bool { return r == '.' || r == '-' || r == '–' || r == '—' }) {
			if hasNonASCII(p) {
				parts = append(parts, splitCJK(p)...)
			} else {
				parts = append(parts, classifyWord(p))
			}
		}
	}
	for _, loc := range reProtect.FindAllStringIndex(c, -1) {
		splitGap(c[pos:loc[0]])
		parts = append(parts, classifyWord(c[loc[0]:loc[1]]))
		pos = loc[1]
	}
	splitGap(c[pos:])
	for i := range parts {
		parts[i].glued = i > 0
	}
	return parts
}

func tokenize(s string) []token {
	chunks, first := splitChunks(normWidth(s))
	var toks []token
	for _, ch := range chunks {
		for _, t := range splitChunk(ch.text) {
			t.group = ch.group
			toks = append(toks, t)
		}
	}
	postPass(toks, first)
	return toks
}

// postPass resolves tokens whose meaning depends on their neighbours.
func postPass(toks []token, releaseGroup int) {
	next := func(i int) int {
		if i+1 < len(toks) && toks[i+1].group == toks[i].group {
			return i + 1
		}
		return -1
	}
	for i := range toks {
		t := &toks[i]
		u := strings.ToUpper(t.text)
		j := next(i)
		switch {
		case (u == "SEASON" || u == "SAISON") && j >= 0 && (toks[j].cat == cNum || toks[j].ord):
			toks[j].cat = cSeason
		case t.ord && j >= 0 && (strings.EqualFold(toks[j].text, "season") || strings.EqualFold(toks[j].text, "saison")):
			t.cat = cSeason
		case epPrefix[u] && j >= 0 && toks[j].cat == cNum:
			t.cat, toks[j].cat = cIdent, cEpisode
		case (titleNumPrefix[u] || t.text == "No" || t.text == "NO") && j >= 0 && toks[j].cat == cNum:
			t.cat, toks[j].cat = cIdent, cIdent
		case t.cat == cType && j >= 0 && (strings.EqualFold(toks[j].text, "ver") || strings.EqualFold(toks[j].text, "version") || toks[j].text == "版"):
			t.cat = cIdent // "[NCOP ver]": a version of the episode, not the NCOP itself
		case t.cat == cNC:
			for k := i + 1; k < len(toks) && k <= i+2; k++ {
				if toks[k].cat == cType && (toks[k].typ == "op" || toks[k].typ == "ed") {
					toks[k].trust = true
					t.cat = cIdent
				}
			}
			if t.cat == cNC { // no OP/ED named: "[无字幕]" = raw, no subtitles
				t.cat = cIdent
			}
		}
	}
	// "03(51)", "12 (176)": a number followed by a bracket holding only another number —
	// the smaller is the episode, the larger an absolute number
	groupSize := map[int]int{}
	for _, t := range toks {
		if t.cat != cVersion {
			groupSize[t.group]++
		}
	}
	for i := 0; i+1 < len(toks); i++ {
		a, b := &toks[i], &toks[i+1]
		strongA := (a.group > 0 && groupSize[a.group] == 1) || (i > 0 && toks[i-1].cat == cDash && toks[i-1].group == a.group)
		if a.cat == cNum && b.cat == cNum && strongA && b.group != a.group && b.group > 0 && groupSize[b.group] == 1 && a.num < b.num {
			b.cat = cIdent
		}
	}
	// a bracket filled by "a - b" (a < b) is a range; by exactly two numbers ("[03_51]",
	// "[12 (176)]"-style) the smaller one is the episode, the other an absolute number
	groups := map[int][]int{}
	for i, t := range toks {
		if t.group > 0 && t.cat != cVersion {
			groups[t.group] = append(groups[t.group], i)
		}
	}
	for _, idx := range groups {
		switch {
		case len(idx) == 3 && toks[idx[0]].cat == cNum && toks[idx[1]].cat == cDash && toks[idx[2]].cat == cNum && toks[idx[0]].num < toks[idx[2]].num:
			toks[idx[0]].cat, toks[idx[1]].cat, toks[idx[2]].cat = cRange, cIdent, cIdent
		case len(idx) == 2 && toks[idx[0]].cat == cNum && toks[idx[1]].cat == cNum && toks[idx[0]].num != toks[idx[1]].num:
			a, b := idx[0], idx[1]
			if toks[b].num < toks[a].num {
				a, b = b, a
			}
			toks[a].pair, toks[b].cat = true, cIdent
		}
	}
	// the release group (a bracket at the very start) carries no episode information,
	// unless it is only a number ("[01] Title.mkv") or a type keyword ("[OVA] Show")
	if releaseGroup > 0 {
		meaningful := false
		for _, i := range groups[releaseGroup] {
			if toks[i].cat != cNum && toks[i].cat != cType && toks[i].cat != cEpisode && toks[i].cat != cRange {
				meaningful = true
			}
		}
		if meaningful {
			for i := range toks {
				if toks[i].group == releaseGroup && toks[i].cat != cRange {
					toks[i].cat = cIdent
				}
			}
		}
	}
}

// trustTypes decides which ambiguous type keywords (SP, OP, ED, Special, PV, CM, Preview …)
// really mark the file type: inside a bracket, after the last " - ", followed by a number
// or by the end of the name part, or when the name has no title words at all. Untrusted
// keywords become title words ("Special Agent Show", "takt op.Destiny").
func trustTypes(toks []token) {
	lastDash := -1
	words := 0
	for i, t := range toks {
		if t.cat == cDash && t.group == 0 {
			lastDash = i
		}
		if t.cat == cWord {
			words++
		}
	}
	for i := range toks {
		t := &toks[i]
		if t.cat != cType || t.trust {
			continue
		}
		switch {
		case t.group > 0, lastDash >= 0 && i > lastDash, words == 0:
			t.trust = true
			continue
		}
		// next token of the same group, skipping seasons and versions
		k := i + 1
		for k < len(toks) && toks[k].group == t.group && (toks[k].cat == cSeason || toks[k].cat == cVersion) {
			k++
		}
		if k >= len(toks) || toks[k].group != t.group || toks[k].cat == cNum || toks[k].cat == cIdent || toks[k].cat == cDash {
			t.trust = true
		}
	}
	for i := range toks {
		t := &toks[i]
		if t.cat == cType && !t.trust {
			t.cat = cWord
		}
		// a trusted keyword without its own number takes the number written right after it
		// ("SP 2", "Special S1 01", "NCED 01")
		if t.cat == cType && !t.hasNum && t.typ != "extra" && t.typ != "sample" {
			k := i + 1
			for k < len(toks) && toks[k].group == t.group && (toks[k].cat == cSeason || toks[k].cat == cVersion) {
				k++
			}
			if k < len(toks) && toks[k].group == t.group && toks[k].cat == cNum && !toks[k].used {
				t.num, t.hasNum = toks[k].num, true
				toks[k].used = true
			}
		}
	}
}

// ---------------------------------------------------------------------------------------
// decision

// candidate tiers, strongest first
const (
	tierNone     = iota
	tierBare     // bare number ("Show 03", "TV S1 (1996) 05")
	tierPair     // smaller of two numbers filling a bracket ("[03_51]")
	tierBracket  // isolated bracket number ("[05]", "[12v2]", "[13END]")
	tierDash     // " - 05"
	tierExplicit // S01E05, EP05, 第5话
)

var tierConf = map[int]float64{tierBare: 0.6, tierPair: 0.7, tierBracket: 0.85, tierDash: 0.9, tierExplicit: 0.9}

// analysis is the parse result of one name with the details ClassifyFiles needs.
type analysis struct {
	ParsedName
	toks    []token
	numIdx  int     // token holding the chosen number, -1 if none
	season  float64 // first season marker, 0 = none
	noNum   bool    // no number candidate at all (an unnumbered special)
	strong  bool    // number from an explicit marker, dash or bracket
	hasType bool    // a trusted type keyword was found
}

// tierOf rates a bare number token. allowBare=false (torrent titles) ignores bare numbers.
func tierOf(toks []token, i int, allowBare bool) int {
	t := toks[i]
	if t.cat == cEpisode {
		return tierExplicit
	}
	if t.cat != cNum || t.used {
		return tierNone
	}
	if i > 0 && toks[i-1].group == t.group && toks[i-1].cat == cDash {
		return tierDash
	}
	if t.group > 0 {
		n := 0
		for _, o := range toks {
			if o.group == t.group && o.cat != cVersion {
				n++
			}
		}
		if n == 1 {
			return tierBracket
		}
	}
	if t.pair {
		return tierPair
	}
	if !allowBare || t.glued {
		return tierNone
	}
	padded := len(t.text) >= 2 && t.text[0] == '0'
	if i == 0 {
		if padded { // "01 - Title.mkv", "01.mkv"; but "86.mkv" is a title
			return tierBare
		}
		return tierNone
	}
	p := toks[i-1]
	if p.group == t.group && (p.cat == cWord || p.cat == cNum) {
		// glued to a title word: only an episode-looking number at the end of the name
		// part ("Show 03", "名侦探光之美少女！ 38"), not "Mob Psycho 100" or "U-17 Kesshou"
		n := reNum.FindStringSubmatch(strings.ToUpper(t.text))
		if n == nil || !(len(n[1]) == 2 || padded) {
			return tierNone
		}
		if i+1 < len(toks) && toks[i+1].group == t.group && (toks[i+1].cat == cWord || toks[i+1].cat == cNum || toks[i+1].cat == cType) {
			return tierNone
		}
		return tierBare
	}
	return tierBare // separated from the title by metadata or a bracket: "Pokemon (2023) 153"
}

func parseName(name string, allowBare bool) analysis {
	toks := tokenize(name)
	trustTypes(toks)
	a := analysis{toks: toks, numIdx: -1}
	for _, t := range toks {
		if t.cat == cSeason && t.hasNum && a.season == 0 {
			a.season = t.num
		}
	}
	a.Season = routeSeason(toks)
	if a.Season == 0 {
		a.Season = makuSeason(name)
	}

	// file type from trusted keywords; priority sample > op/ed > extra > pv > cm > sp
	kinds := map[string]bool{}
	for _, t := range toks {
		if t.cat == cType {
			kinds[t.typ] = true
		}
	}
	kind := ""
	switch {
	case kinds["sample"]:
		kind = "extra"
	case kinds["oped"], kinds["op"] && kinds["ed"]:
		kind = "extra" // one video with both: cannot be one episode key
	case kinds["op"]:
		kind = "op"
	case kinds["ed"]:
		kind = "ed"
	case kinds["extra"]:
		kind = "extra"
	case kinds["pv"]:
		kind = "pv"
	case kinds["cm"]:
		kind = "cm"
	case kinds["sp"]:
		kind = "sp"
	}
	a.hasType = kind != ""
	if kind == "extra" {
		a.Kind, a.Confidence = "extra", 0.5
		return a
	}

	// episode candidates by tier
	best := tierNone
	var cands []int
	for i := range toks {
		tr := tierOf(toks, i, allowBare)
		if tr == tierNone {
			continue
		}
		if tr > best {
			best, cands = tr, nil
		}
		if tr == best {
			cands = append(cands, i)
		}
	}
	distinct := func(idx []int) []int {
		var out []int
		seen := map[float64]bool{}
		for _, i := range idx {
			if !seen[toks[i].num] {
				seen[toks[i].num] = true
				out = append(out, i)
			}
		}
		return out
	}

	if kind != "" {
		a.Kind = kind
		// the keyword's own number ("SP02", "Special S1 01")
		var own []float64
		ownIdx := -1
		for i, t := range toks {
			if t.cat == cType && t.typ == kind && t.hasNum {
				if !containsF(own, t.num) {
					own = append(own, t.num)
				}
				ownIdx = i
			}
		}
		switch {
		case len(own) == 1:
			a.Episode, a.Confidence, a.numIdx, a.strong, a.Sure = ptrF(own[0]), 0.6, ownIdx, true, true
		case len(own) > 1:
			a.Confidence = 0.2 // "SP1 SP2": ambiguous
		default:
			// the single remaining number (any tier)
			var all []int
			for i := range toks {
				if tierOf(toks, i, allowBare) != tierNone {
					all = append(all, i)
				}
			}
			all = distinct(all)
			switch {
			case len(all) == 1:
				a.Episode, a.Confidence, a.numIdx = ptrF(toks[all[0]].num), 0.5, all[0]
				a.strong = tierOf(toks, all[0], allowBare) >= tierBracket
				a.Sure = a.strong
			case len(all) == 0:
				a.Confidence, a.noNum = 0.3, true
			default:
				a.Confidence = 0.2
			}
		}
		if a.Episode != nil && (*a.Episode < 0 || *a.Episode > MaxSpecialNumber) {
			a.Episode, a.numIdx = nil, -1
		}
		return a
	}

	cands = distinct(cands)
	switch {
	case len(cands) == 1:
		v := toks[cands[0]].num
		if v > MaxMainEpisode {
			a.Kind = "other"
			break
		}
		a.Kind, a.Episode, a.Confidence, a.numIdx = "episode", ptrF(v), tierConf[best], cands[0]
		a.strong = best >= tierBracket
		a.Sure = a.strong
	case len(cands) > 1:
		a.Kind, a.Confidence = "episode", 0.2 // several different candidates: ambiguous
	default: // nothing left (or only a batch range "01-12")
		a.Kind, a.noNum = "other", true
	}
	return a
}

func containsF(xs []float64, v float64) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func ptrF(v float64) *float64 { return &v }

// stem strips the extension of a file name (only a real extension: "Show - 12.5" keeps .5).
func stem(name string) string {
	base := path.Base(name)
	ext := path.Ext(base)
	if len(ext) >= 2 && len(ext) <= 6 && strings.IndexFunc(ext[1:], unicode.IsLetter) >= 0 && !strings.ContainsAny(ext, " ]】)") {
		return strings.TrimSuffix(base, ext)
	}
	return base
}

// ParseEpisode extracts kind and episode number from a fansub/raw release file name (the
// base name of the path). Unnumbered or ambiguous specials have Episode == nil.
func ParseEpisode(name string) ParsedName { return parseName(stem(name), true).ParsedName }

// ParseTitle parses a torrent title (not a file name): like ParseEpisode but bare numbers
// ("Show 03") are not accepted, which is too ambiguous for titles.
func ParseTitle(title string) ParsedName { return parseName(title, false).ParsedName }

// ---------------------------------------------------------------------------------------
// folders, torrent title and siblings

// extra words that mark a folder of bonus material anywhere in its name
var reExtraDirCJK = regexp.MustCompile(`特典|映像特典|予告|预告|番宣|无字|無字|花絮|ノンテロップ|ノンクレジット`)

var extraDirWords = setOf("EXTRA", "EXTRAS", "BONUS", "BONUSES", "MENU", "MENUS", "PREVIEW", "PREVIEWS", "SAMPLE", "SAMPLES", "SCANS", "SCAN", "NC",
	"NCOPED", "OTHER", "OTHERS", "MAKING", "INTERVIEW", "INTERVIEWS", "TOKUTEN", "FEATURETTES", "OMAKE")

// dirKind classifies a folder name: sp|op|ed|pv|cm|extra or "". A folder counts only if,
// apart from metadata ("Special (2S7E)_DVDrip_480P", "OVA 1994~1995", "Extras (TV S1 1996)"),
// it holds nothing but the keyword — so a show folder "Special Agent Show [BD]" does not.
func dirKind(d string) (string, float64) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", 0
	}
	ts := tokenize(d)
	season := 0.0
	kind := ""
	for _, t := range ts {
		u := strings.ToUpper(t.text)
		switch {
		case t.cat == cSeason && t.hasNum:
			if season == 0 {
				season = t.num
			}
		case extraDirWords[u] || u == "NCOP&NCED" || (t.cat == cType && (t.typ == "extra" || t.typ == "sample" || t.typ == "oped" || t.typ == "nc")) || t.cat == cNC:
			if kind == "" {
				kind = "extra"
			}
		case t.cat == cType:
			if kind == "" {
				kind = t.typ
			}
		case t.cat == cWord:
			if reExtraDirCJK.MatchString(t.text) {
				return "extra", season
			}
			if !t.ord && !strings.EqualFold(t.text, "ver") {
				return "", season
			}
		}
	}
	if kind == "" && reExtraDirCJK.MatchString(d) {
		kind = "extra"
	}
	return kind, season
}

// fileInfo is what ClassifyFiles needs besides the ParsedName.
type fileInfo struct {
	video  bool
	dir    string
	sig    string // title words before the number + seasons: identifies the content
	noNum  bool   // unnumbered special
	strong bool
}

// ClassifyFile decides the kind (episode|sp|op|ed|pv|cm|extra|other) and episode number of
// one file of a torrent. Non-video files are "other". Specials (sp, NCOP = op, NCED = ed,
// pv, cm) keep their number and are mapped like episodes (episode key "sp1", "ncop1", …);
// unnumbered or ambiguous ones have Episode == nil. Menu, Sample, Preview and 特典 material
// are "extra" (never mapped). title is the torrent title, used when the torrent has a single
// video whose own name carries no number. Prefer ClassifyFiles for a whole torrent.
func ClassifyFile(p, title string, videos int) ParsedName {
	r, _ := classifyOne(p, title, videos)
	return r
}

func classifyOne(p, title string, videos int) (ParsedName, fileInfo) {
	r, info := classifyOneKind(p, title, videos)
	if IsVideo(p) {
		r.Season = 0
		for _, h := range pathHints(p) {
			if h.Season > 0 {
				r.Season = h.Season
				break
			}
		}
	}
	return r, info
}

func classifyOneKind(p, title string, videos int) (ParsedName, fileInfo) {
	if !IsVideo(p) {
		return ParsedName{Kind: "other"}, fileInfo{}
	}
	clean := strings.ReplaceAll(p, "\\", "/")
	dirs := strings.Split(clean, "/")
	name := dirs[len(dirs)-1]
	dirs = dirs[:len(dirs)-1]
	a := parseName(stem(name), true)
	info := fileInfo{video: true, dir: strings.Join(dirs, "/"), noNum: a.noNum, strong: a.strong}

	// signature: dir words, seasons, and the name's title words before the number
	var sig []string
	for _, d := range dirs {
		_, s := dirKind(d)
		if s > 0 {
			sig = append(sig, "s"+fmtNum(s))
		}
	}
	if a.season > 0 {
		sig = append(sig, "s"+fmtNum(a.season))
	}
	if a.numIdx >= 0 {
		for i := 0; i < a.numIdx; i++ {
			if t := a.toks[i]; t.cat == cWord {
				sig = append(sig, strings.ToLower(t.text))
			}
		}
	}
	info.sig = strings.Join(sig, " ")

	r := a.ParsedName
	switch r.Kind {
	case "op", "ed", "pv", "cm", "extra":
		return r, info
	}
	for _, d := range dirs {
		dk, _ := dirKind(d)
		switch dk {
		case "":
			continue
		case "extra":
			return ParsedName{Kind: "extra", Confidence: 0.5}, info
		}
		// a folder of one special kind: the file name may only carry a number
		if r.Episode != nil {
			return ParsedName{Kind: dk, Episode: r.Episode, Confidence: 0.5, Sure: r.Sure}, info
		}
		info.noNum = r.Kind == "other" || a.noNum
		return ParsedName{Kind: dk, Confidence: 0.3}, info
	}
	if r.Episode == nil && videos == 1 && title != "" {
		t := ParseTitle(title)
		switch {
		case r.Kind == "other" && (t.Kind == "episode" || t.Kind == "sp"):
			r = t
			r.Sure = false // inferred from the torrent title
		case r.Kind == t.Kind && t.Episode != nil:
			r.Episode, r.Confidence, r.Sure = t.Episode, t.Confidence*0.8, false
		}
	}
	return r, info
}

// ClassifyFiles classifies all files of one torrent (paths in torrent order; title = the
// torrent title) and cross-checks them:
//
//   - a special kind (sp/op/ed/pv/cm) with a single, unnumbered file in the whole torrent is
//     number 1 ("Show - NCOP.mkv" next to the episodes is ncop1); with several unnumbered
//     files of the kind they all stay without a number;
//   - two files with the same (kind, number) whose content differs (other title words,
//     other season — not just CHS/CHT or 1080p/720p variants) are ambiguous: Episode=nil,
//     so they are not both mapped as the same episode ("TV S1 05" and "TV S2 05");
//   - consecutive numbers of one special kind inside one folder raise the confidence.
func ClassifyFiles(paths []string, title string) []ParsedName {
	return classifyFilesIn(paths, title, nil)
}

// classifyFilesIn is ClassifyFiles with every file assigned to a target group (the work
// it will be mapped to; nil = one group): the cross-checks (clashes, the only unnumbered
// special of a kind, runs) only compare files of the same group, so "TV S1/05" and
// "TV S2/05" routed to two different works no longer clash.
func classifyFilesIn(paths []string, title string, groups []string) []ParsedName {
	groupOf := func(i int) string {
		if groups == nil {
			return ""
		}
		return groups[i]
	}
	videos := 0
	for _, p := range paths {
		if IsVideo(p) {
			videos++
		}
	}
	out := make([]ParsedName, len(paths))
	infos := make([]fileInfo, len(paths))
	for i, p := range paths {
		out[i], infos[i] = classifyOne(p, title, videos)
	}

	type key struct {
		group, kind string
		num         float64
	}
	type gk struct{ group, kind string }
	byKey := map[key][]int{}
	perKind := map[gk][]int{}
	for i, r := range out {
		if !infos[i].video || !MappableKind(r.Kind) {
			continue
		}
		g := groupOf(i)
		perKind[gk{g, r.Kind}] = append(perKind[gk{g, r.Kind}], i)
		if r.Episode != nil {
			k := key{g, r.Kind, *r.Episode}
			byKey[k] = append(byKey[k], i)
		}
	}
	// same key, different content → ambiguous. The numbering of every content group taking
	// part in a clash is unreliable as a whole ("TV S1 01-51" next to "TV S2 01-26": S1 27-51
	// do not clash but are as season-relative as the rest), so all of them lose their number.
	type ks struct{ group, kind, sig string }
	clash := map[ks]bool{}
	for k, idx := range byKey {
		for _, i := range idx[1:] {
			if infos[i].sig != infos[idx[0]].sig {
				for _, j := range idx {
					clash[ks{k.group, k.kind, infos[j].sig}] = true
				}
				break
			}
		}
	}
	if len(clash) > 0 {
		for i, r := range out {
			if infos[i].video && r.Episode != nil && clash[ks{groupOf(i), r.Kind, infos[i].sig}] {
				out[i].Episode, out[i].Confidence, out[i].Sure = nil, 0.2, false
			}
		}
	}
	// the only unnumbered special of its kind → 1
	for k, idx := range perKind {
		if k.kind == "episode" || len(idx) != 1 {
			continue
		}
		if i := idx[0]; out[i].Episode == nil && infos[i].noNum {
			out[i].Episode, out[i].Confidence, out[i].Sure = ptrF(1), 0.4, false
		}
	}
	// consecutive numbering of a special kind inside one folder supports the numbers
	type dk struct{ group, dir, kind string }
	runs := map[dk][]int{}
	for i, r := range out {
		if infos[i].video && r.Episode != nil && r.Kind != "episode" && MappableKind(r.Kind) {
			k := dk{groupOf(i), infos[i].dir, r.Kind}
			runs[k] = append(runs[k], i)
		}
	}
	for _, idx := range runs {
		if len(idx) < 2 {
			continue
		}
		nums := make([]float64, 0, len(idx))
		for _, i := range idx {
			nums = append(nums, *out[i].Episode)
		}
		sort.Float64s(nums)
		consecutive := true
		for j := 1; j < len(nums); j++ {
			if nums[j]-nums[j-1] > 1 {
				consecutive = false
			}
		}
		if consecutive {
			for _, i := range idx {
				out[i].Confidence = max(out[i].Confidence, 0.6)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------
// routing hints (which work of a multi-season / multi-work batch a file belongs to, see
// route.go)

// routeHint is what one name (file stem or folder) says about the work it belongs to.
type routeHint struct {
	Season float64 // TV season marker (see ParsedName.Season), 0 = none
	Type   string  // movie|ova|tv from keywords (劇場版/Movie/Film, OVA/OAD, TV); "" = none or mixed ("TV+OVA+Film")
	Years  []int   // 4-digit years written in the name (1994~1995, 1999-04-24, (2001))
	Norm   string  // normName of the whole name
}

var reSeasonEp = regexp.MustCompile(`^S(\d{1,2})E\d`)

// reMaku: "Dai Ni Maku" / "Dai_Ni_Maku" (romanised 第二幕, "act two" = the second part /
// season of a work; separators may be spaces, _ . ~ -)
var reMaku = regexp.MustCompile(`(?i)(?:^|[^a-z])dai[\s_.~-]*(ichi|ni|san|yon|shi|go)[\s_.~-]*maku(?:[^a-z]|$)`)

var makuNumbers = map[string]float64{"ichi": 1, "ni": 2, "san": 3, "yon": 4, "shi": 4, "go": 5}

// makuSeason is the season a romanised "Dai N Maku" states (0 = none).
func makuSeason(s string) float64 {
	if m := reMaku.FindStringSubmatch(s); m != nil {
		return makuNumbers[strings.ToLower(m[1])]
	}
	return 0
}

var reOVAWord = regexp.MustCompile(`^(?:OVA|OAD|OAV)S?\d{0,3}$`)

// routeSeason is the single TV season written in a name: S2, Season 2, 2nd Season, 第二季,
// S02E05, a Roman numeral suffix (Ⅱ, "Show II"). Season markers numbering a special
// ("Special S2", "OVA S1") are skipped; several different seasons ("S1+S2") give 0.
func routeSeason(toks []token) float64 {
	var found []float64
	add := func(v float64) {
		if v > 0 && v < 100 && !containsF(found, v) {
			found = append(found, v)
		}
	}
	for i, t := range toks {
		switch {
		case t.cat == cSeason && t.hasNum:
			if i > 0 && toks[i-1].group == t.group {
				u := strings.ToUpper(toks[i-1].text)
				if typeWords[u] == "sp" || reOVAWord.MatchString(u) {
					continue // "Special S1 01": the specials' own numbering
				}
			}
			add(t.num)
		case t.cat == cEpisode:
			if m := reSeasonEp.FindStringSubmatch(strings.ToUpper(t.text)); m != nil {
				v, _ := strconv.ParseFloat(m[1], 64)
				add(v)
			}
		case t.cat == cWord:
			if v := romanSuffix(toks, i); v > 0 {
				add(v)
			}
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return 0
}

// romanSuffix: "とある魔術の禁書目録Ⅱ", "Show II" (a title word, then a standalone II/III/IV
// ending the words of its group) → 2/3/4.
func romanSuffix(toks []token, i int) float64 {
	t := toks[i]
	if r, _ := utf8.DecodeLastRuneInString(t.text); r >= 'Ⅱ' && r <= 'Ⅹ' {
		if utf8.RuneCountInString(t.text) > 1 || (i > 0 && toks[i-1].group == t.group && toks[i-1].cat == cWord) {
			return float64(r-'Ⅰ') + 1
		}
	}
	v := map[string]float64{"II": 2, "III": 3, "IV": 4}[t.text]
	if v == 0 || i == 0 || toks[i-1].group != t.group || toks[i-1].cat != cWord {
		return 0
	}
	if i+1 < len(toks) && toks[i+1].group == t.group && toks[i+1].cat == cWord {
		return 0 // "Show II Something": part of a title
	}
	return v
}

// nameHint parses one file stem or folder name for routing.
func nameHint(s string) routeHint {
	toks := tokenize(s)
	trustTypes(toks)
	h := routeHint{Season: routeSeason(toks), Norm: normName(s), Years: yearsIn(s)}
	if h.Season == 0 {
		h.Season = makuSeason(s)
	}
	types := map[string]bool{}
	for _, t := range toks {
		u := strings.ToUpper(t.text)
		switch {
		case u == "MOVIE" || u == "MOVIES" || u == "FILM" || u == "FILMS" || u == "GEKIJOUBAN" || strings.Contains(t.text, "剧场版") || strings.Contains(t.text, "劇場版"):
			types["movie"] = true
		case reOVAWord.MatchString(u):
			types["ova"] = true
		case u == "TV" || strings.HasPrefix(t.text, "TV动画") || strings.HasPrefix(t.text, "TVアニメ"):
			types["tv"] = true
		}
	}
	if len(types) == 1 {
		for k := range types {
			h.Type = k
		}
	}
	return h
}

// pathHints: the file stem first, then its folders from the innermost outwards.
func pathHints(p string) []routeHint {
	parts := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	out := make([]routeHint, 0, len(parts))
	out = append(out, nameHint(stem(parts[len(parts)-1])))
	for i := len(parts) - 2; i >= 0; i-- {
		if strings.TrimSpace(parts[i]) != "" {
			out = append(out, nameHint(parts[i]))
		}
	}
	return out
}

// yearsIn lists the 4-digit runs 1900–2099 of s.
func yearsIn(s string) []int {
	var out []int
	for i := 0; i < len(s); {
		if s[i] < '0' || s[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j-i == 4 {
			if v, _ := strconv.Atoi(s[i:j]); v >= 1900 && v < 2100 {
				out = append(out, v)
			}
		}
		i = j
	}
	return out
}

// normName folds a title for substring comparison: full-width to ASCII, lower case, only
// letters and digits ("You`re Under Arrest!" → "youreunderarrest").
func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(normWidth(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
