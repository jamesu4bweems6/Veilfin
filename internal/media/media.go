// Package media 封装对 ffmpeg / ffprobe 的调用。
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Find 在 dir（可选）和 PATH 中查找 ffmpeg / ffprobe。
func Find(dir string) (*Tools, error) {
	look := func(name string) (string, error) {
		if dir != "" {
			for _, n := range []string{name, name + ".exe"} {
				p := filepath.Join(dir, n)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					return p, nil
				}
			}
		}
		return exec.LookPath(name)
	}
	ff, err := look("ffmpeg")
	if err != nil {
		return nil, errors.New("找不到 ffmpeg，请安装或用 -ffmpeg 指定所在目录")
	}
	fp, err := look("ffprobe")
	if err != nil {
		return nil, errors.New("找不到 ffprobe，请安装或用 -ffmpeg 指定所在目录")
	}
	return &Tools{FFmpeg: ff, FFprobe: fp}, nil
}

func run(ctx context.Context, timeout time.Duration, stdin []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("%s 失败: %v %s", filepath.Base(name), err, msg)
	}
	return out.Bytes(), nil
}

// ProbeResult 是 ffprobe 的结果。
type ProbeResult struct {
	Duration  float64
	Width     int
	Height    int
	VCodec    string
	ACodec    string
	Container string
	Bitrate   int64
	Subs      []SubStream
}

// SubStream 是内嵌字幕流。
type SubStream struct {
	Index   int
	Codec   string
	Lang    string
	Title   string
	Default bool
	Text    bool // 文本字幕才能转成 WebVTT；图形字幕（PGS 等）跳过
}

var textSubCodecs = map[string]bool{"subrip": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true}

func (t *Tools) Probe(ctx context.Context, path string) (*ProbeResult, error) {
	out, err := run(ctx, 60*time.Second, nil, t.FFprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			BitRate    string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			Index       int               `json:"index"`
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Width       int               `json:"width"`
			Height      int               `json:"height"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	r := &ProbeResult{Container: raw.Format.FormatName}
	r.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	r.Bitrate, _ = strconv.ParseInt(raw.Format.BitRate, 10, 64)
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if r.VCodec == "" && s.Disposition["attached_pic"] == 0 {
				r.VCodec, r.Width, r.Height = s.CodecName, s.Width, s.Height
			}
		case "audio":
			if r.ACodec == "" {
				r.ACodec = s.CodecName
			}
		case "subtitle":
			r.Subs = append(r.Subs, SubStream{
				Index: s.Index, Codec: s.CodecName, Lang: s.Tags["language"], Title: s.Tags["title"],
				Default: s.Disposition["default"] == 1, Text: textSubCodecs[s.CodecName],
			})
		}
	}
	if math.IsNaN(r.Duration) || r.Duration < 0 {
		r.Duration = 0
	}
	return r, nil
}

// Frame 截取 at 秒处的一帧，输出 JPEG（宽度 width，0 表示原尺寸）。
func (t *Tools) Frame(ctx context.Context, path string, at float64, width int) ([]byte, error) {
	args := []string{"-v", "error", "-ss", strconv.FormatFloat(math.Max(at, 0), 'f', 3, 64), "-i", path, "-frames:v", "1"}
	if width > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale='min(%d,iw)':-2", width))
	}
	args = append(args, "-q:v", "3", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	out, err := run(ctx, 60*time.Second, nil, t.FFmpeg, args...)
	if err == nil && len(out) == 0 {
		err = errors.New("截图为空（时间点可能超出视频长度）")
	}
	return out, err
}

// TrickplayInfo 描述进度条预览图的布局。
type TrickplayInfo struct {
	Interval float64 `json:"interval"` // 每张缩略图间隔秒数
	Cols     int     `json:"cols"`
	Rows     int     `json:"rows"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Count    int     `json:"count"` // 缩略图总数
}

// Trickplay 生成进度条预览雪碧图，写入 outDir，返回布局信息和按顺序排列的文件路径。
// 只解码关键帧（-skip_frame nokey），3 小时的视频也能很快完成。
func (t *Tools) Trickplay(ctx context.Context, path string, duration float64, vw, vh int, outDir string) (*TrickplayInfo, []string, error) {
	if duration <= 0 {
		return nil, nil, errors.New("时长未知，无法生成预览图")
	}
	info := &TrickplayInfo{Interval: 10, Cols: 10, Rows: 10, Width: 240}
	if n := duration / info.Interval; n > 2000 {
		info.Interval = math.Ceil(duration / 2000)
	}
	info.Height = 135
	if vw > 0 && vh > 0 {
		info.Height = int(math.Round(float64(info.Width)*float64(vh)/float64(vw)/2)) * 2
	}
	info.Count = int(math.Ceil(duration / info.Interval))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, nil, err
	}
	pattern := filepath.Join(outDir, "sprite_%03d.jpg")
	vf := fmt.Sprintf("fps=1/%g,scale=%d:%d,tile=%dx%d", info.Interval, info.Width, info.Height, info.Cols, info.Rows)
	_, err := run(ctx, 30*time.Minute, nil, t.FFmpeg, "-v", "error", "-skip_frame", "nokey", "-i", path,
		"-an", "-sn", "-vf", vf, "-fps_mode", "vfr", "-q:v", "5", "-y", pattern)
	if err != nil {
		return nil, nil, err
	}
	files, _ := filepath.Glob(filepath.Join(outDir, "sprite_*.jpg"))
	sort.Strings(files)
	if len(files) == 0 {
		return nil, nil, errors.New("没有生成预览图")
	}
	// 只解码关键帧时，实际得到的帧数可能略少于预期，按文件数修正上限
	if maxN := len(files) * info.Cols * info.Rows; info.Count > maxN {
		info.Count = maxN
	}
	return info, files, nil
}

// ExtractSubtitle 把内嵌文本字幕流转换成 WebVTT。
func (t *Tools) ExtractSubtitle(ctx context.Context, path string, index int) ([]byte, error) {
	return run(ctx, 5*time.Minute, nil, t.FFmpeg, "-v", "error", "-i", path, "-map", "0:"+strconv.Itoa(index), "-f", "webvtt", "pipe:1")
}

// ConvertSubtitle 把外挂字幕（srt/ass/ssa/vtt）转换成 UTF-8 的 WebVTT，自动处理 GBK / UTF-16 编码。
func (t *Tools) ConvertSubtitle(ctx context.Context, file string) ([]byte, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	text, err := ToUTF8(raw)
	if err != nil {
		return nil, err
	}
	format := map[string]string{".srt": "srt", ".ass": "ass", ".ssa": "ass", ".vtt": "webvtt"}[strings.ToLower(filepath.Ext(file))]
	if format == "" {
		return nil, errors.New("不支持的字幕格式")
	}
	return run(ctx, 2*time.Minute, text, t.FFmpeg, "-v", "error", "-f", format, "-i", "pipe:0", "-f", "webvtt", "pipe:1")
}

// ToUTF8 识别常见编码（UTF-8、UTF-16 BOM、GB18030/GBK）并转成 UTF-8。
func ToUTF8(b []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return b[3:], nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		out, _, err := transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder(), b)
		return out, err
	case utf8.Valid(b):
		return b, nil
	default:
		out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), b)
		return out, err
	}
}
