// Package scraper 负责从文件名识别番号 / 标题，并通过在线数据源获取元数据。
package scraper

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// CodeInfo 是从文件名识别出的番号信息。
type CodeInfo struct {
	Code     string // 规范化后的番号，如 ABC-123、FC2-PPV-1234567
	Subtitle bool   // 文件名带 -C / -UC 等中文字幕标记
	Part     int    // 分段（CD1 / -A 等），0 表示不分段
}

var (
	reFC2     = regexp.MustCompile(`(?i)FC2[-_ ]*(?:PPV[-_ ]*)?(\d{5,8})`)
	reHeyzo   = regexp.MustCompile(`(?i)HEYZO[-_ ]*(\d{4})`)
	reNumeric = regexp.MustCompile(`(?:^|[^\d])(\d{6})[-_](\d{2,3})(?:[^\d]|$)`)
	reStd     = regexp.MustCompile(`(?i)(?:^|[^a-z])([a-z]{2,6})[-_ ]?(\d{2,5})(?:[^\d]|$)`)
	reContent = regexp.MustCompile(`(?i)(?:^|[^a-z\d])\d{0,3}([a-z]{2,6})(00\d{3})(?:[^\d]|$)`) // DMM 内容 ID：abp00123
	rePart    = regexp.MustCompile(`(?i)(?:cd|part|pt|disc)[-_ ]?(\d)\b|[-_ ]([ab])$`)          // -C 通常表示中文字幕，不当作分段
	reSubFlag = regexp.MustCompile(`(?i)[-_ ](?:c|uc|ch|sub)$|中文字幕|中字`)
	reNoise   = regexp.MustCompile(`(?i)\b(?:\d{3,4}p|[hx]\.?26[45]|hevc|avc|aac|ac3|dts|flac|hdr|web-?dl|webrip|bluray|bdrip|uncensored|leak|4k|8k|60fps)\b`)
	reSite    = regexp.MustCompile(`(?i)^(?:\[[^\]]*\]|【[^】]*】|[a-z0-9.-]+\.(?:com|net|org|tv|cc|me|xyz)@?)\s*`)
)

// 容易被误识别为番号前缀的词
var stopPrefixes = map[string]bool{
	"MP": true, "MP4": true, "AVI": true, "MKV": true, "WEB": true, "HD": true, "FHD": true, "UHD": true,
	"CD": true, "PART": true, "DISC": true, "HEVC": true, "AVC": true, "AAC": true, "DTS": true, "FPS": true,
	"HDR": true, "SDR": true, "BD": true, "DVD": true, "VOL": true, "EP": true, "EPS": true, "SEASON": true,
}

// ParseCode 从文件名（可含路径和扩展名）识别番号。识别不到时 Code 为空。
func ParseCode(name string) CodeInfo {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	for {
		cleaned := reSite.ReplaceAllString(base, "")
		if cleaned == base {
			break
		}
		base = cleaned
	}
	var info CodeInfo
	if m := rePart.FindStringSubmatch(base); m != nil {
		if m[1] != "" {
			info.Part, _ = strconv.Atoi(m[1])
		} else if m[2] != "" {
			info.Part = int(strings.ToLower(m[2])[0]-'a') + 1
		}
	}
	info.Subtitle = reSubFlag.MatchString(base)
	clean := reNoise.ReplaceAllString(base, " ")

	if m := reFC2.FindStringSubmatch(clean); m != nil {
		info.Code = "FC2-PPV-" + m[1]
		return info
	}
	if m := reHeyzo.FindStringSubmatch(clean); m != nil {
		info.Code = "HEYZO-" + m[1]
		return info
	}
	if m := reNumeric.FindStringSubmatch(clean); m != nil {
		sep := "-"
		if strings.Contains(clean, m[1]+"_") {
			sep = "_"
		}
		info.Code = m[1] + sep + m[2]
		return info
	}
	for _, m := range reStd.FindAllStringSubmatch(clean, -1) {
		prefix := strings.ToUpper(m[1])
		if stopPrefixes[prefix] {
			continue
		}
		info.Code = prefix + "-" + trimZeros(m[2])
		return info
	}
	if m := reContent.FindStringSubmatch(clean); m != nil {
		prefix := strings.ToUpper(m[1])
		if !stopPrefixes[prefix] {
			info.Code = prefix + "-" + strings.TrimPrefix(m[2], "00")
		}
	}
	return info
}

// trimZeros 去掉多余的前导零，但至少保留 3 位（ABP-00123 → ABP-123，SSIS-001 不变）。
func trimZeros(d string) string {
	for len(d) > 3 && d[0] == '0' {
		d = d[1:]
	}
	return d
}

// NormalizeCode 把番号统一成可比较的形式（去掉分隔符、大写、去掉数字前导零）。
func NormalizeCode(code string) string {
	up := strings.ToUpper(code)
	var letters, digits strings.Builder
	for _, r := range up {
		switch {
		case r >= 'A' && r <= 'Z':
			if digits.Len() > 0 { // 字母出现在数字之后（如 FC2-PPV），按原样保留
				letters.WriteString(digits.String())
				digits.Reset()
			}
			letters.WriteRune(r)
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		}
	}
	d := strings.TrimLeft(digits.String(), "0")
	return letters.String() + d
}

var (
	reYear     = regexp.MustCompile(`(?:^|[^\d])((?:19|20)\d{2})(?:[^\d]|$)`)
	reBrackets = regexp.MustCompile(`\[[^\]]*\]|【[^】]*】|\([^)]*\)|（[^）]*）`)
	reSeps     = regexp.MustCompile(`[._]+`)
	reSpaces   = regexp.MustCompile(`\s+`)
)

// ParseTitle 从文件名提取用于搜索的标题和年份（普通电影用）。
func ParseTitle(name string) (title string, year int) {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if m := reYear.FindStringSubmatch(base); m != nil {
		year, _ = strconv.Atoi(m[1])
	}
	t := reBrackets.ReplaceAllString(base, " ")
	if year > 0 {
		if i := strings.Index(t, strconv.Itoa(year)); i > 0 {
			t = t[:i]
		}
	}
	t = reSeps.ReplaceAllString(t, " ")
	t = reNoise.ReplaceAllString(t, " ")
	t = strings.TrimSpace(reSpaces.ReplaceAllString(t, " "))
	if t == "" {
		t = strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	}
	return t, year
}
