package library

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"veilfin/internal/db"
	"veilfin/internal/media"
)

// nfoMovie 对应 Kodi / Emby / Jellyfin 的 movie.nfo 格式（只取用得到的字段）。
type nfoMovie struct {
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle"`
	SortTitle     string   `xml:"sorttitle"`
	Plot          string   `xml:"plot"`
	Outline       string   `xml:"outline"`
	Year          string   `xml:"year"`
	Premiered     string   `xml:"premiered"`
	ReleaseDate   string   `xml:"releasedate"`
	Studio        []string `xml:"studio"`
	Maker         string   `xml:"maker"`
	Director      []string `xml:"director"`
	Rating        string   `xml:"rating"`
	Ratings       struct {
		Rating []struct {
			Value string `xml:"value"`
		} `xml:"rating"`
	} `xml:"ratings"`
	Set struct {
		Name string `xml:"name"`
	} `xml:"set"`
	Genre  []string `xml:"genre"`
	Tag    []string `xml:"tag"`
	Num    string   `xml:"num"`
	Actors []struct {
		Name string `xml:"name"`
	} `xml:"actor"`
}

// sidecar 是自制视频可以使用的 <文件名>.json 元数据文件。
type sidecar struct {
	Title    string   `json:"title"`
	Overview string   `json:"overview"`
	Date     string   `json:"date"`
	Year     int      `json:"year"`
	Series   string   `json:"series"`
	Studio   string   `json:"studio"`
	Director string   `json:"director"`
	Rating   float64  `json:"rating"`
	Genres   []string `json:"genres"`
	Tags     []string `json:"tags"`
	Actors   []string `json:"actors"`
}

// LocalMeta 是从本地文件读到的元数据。
type LocalMeta struct {
	Meta   db.Meta
	Fields []string // 实际读到的字段
	Source string   // nfo | json
	Poster string   // 图片文件路径
	Back   string
	Thumb  string
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// readLocalMeta 读取视频旁边的 NFO / JSON 和图片。onlyVideoInDir 表示该目录下只有这一个视频，
// 此时目录级的 poster.jpg / fanart.jpg 也归属于它。
func readLocalMeta(videoPath string, onlyVideoInDir bool) *LocalMeta {
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	lm := &LocalMeta{}

	nfoCands := []string{filepath.Join(dir, base+".nfo")}
	if onlyVideoInDir {
		nfoCands = append(nfoCands, filepath.Join(dir, "movie.nfo"))
	}
	for _, p := range nfoCands {
		if parseNFO(p, lm) {
			break
		}
	}
	if lm.Source == "" {
		parseSidecar(filepath.Join(dir, base+".json"), lm)
	}

	imgExts := []string{".jpg", ".jpeg", ".png", ".webp"}
	find := func(names ...string) string {
		for _, n := range names {
			for _, e := range imgExts {
				p := filepath.Join(dir, n+e)
				if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
					return p
				}
			}
		}
		return ""
	}
	lm.Poster = find(base+"-poster", base+".poster")
	lm.Back = find(base+"-fanart", base+"-backdrop", base+".fanart")
	lm.Thumb = find(base+"-thumb", base+"-landscape", base)
	if onlyVideoInDir {
		if lm.Poster == "" {
			lm.Poster = find("poster", "folder", "cover")
		}
		if lm.Back == "" {
			lm.Back = find("fanart", "backdrop", "background", "background1")
		}
		if lm.Thumb == "" {
			lm.Thumb = find("thumb", "landscape")
		}
	}
	return lm
}

func parseNFO(path string, lm *LocalMeta) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	raw, _ = media.ToUTF8(raw)
	var n nfoMovie
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) {
		return r, nil // 已统一转为 UTF-8
	}
	if err := dec.Decode(&n); err != nil {
		return false
	}
	m := &lm.Meta
	set := func(field string, ok bool) {
		if ok {
			lm.Fields = append(lm.Fields, field)
		}
	}
	m.Title = strings.TrimSpace(n.Title)
	set("title", m.Title != "")
	m.OriginalTitle = strings.TrimSpace(n.OriginalTitle)
	set("originalTitle", m.OriginalTitle != "")
	m.SortTitle = strings.TrimSpace(n.SortTitle)
	set("sortTitle", m.SortTitle != "")
	m.Overview = firstNonEmpty(n.Plot, n.Outline)
	set("overview", m.Overview != "")
	m.ReleaseDate = firstNonEmpty(n.Premiered, n.ReleaseDate)
	if len(m.ReleaseDate) > 10 {
		m.ReleaseDate = m.ReleaseDate[:10]
	}
	set("releaseDate", m.ReleaseDate != "")
	m.Year, _ = strconv.Atoi(strings.TrimSpace(n.Year))
	if m.Year == 0 && len(m.ReleaseDate) >= 4 {
		m.Year, _ = strconv.Atoi(m.ReleaseDate[:4])
	}
	set("year", m.Year > 0)
	m.Studio = firstNonEmpty(append(n.Studio, n.Maker)...)
	set("studio", m.Studio != "")
	m.Director = firstNonEmpty(n.Director...)
	set("director", m.Director != "")
	m.Series = strings.TrimSpace(n.Set.Name)
	set("series", m.Series != "")
	m.Code = strings.ToUpper(strings.TrimSpace(n.Num))
	set("code", m.Code != "")
	rating := n.Rating
	if rating == "" && len(n.Ratings.Rating) > 0 {
		rating = n.Ratings.Rating[0].Value
	}
	m.Rating, _ = strconv.ParseFloat(strings.TrimSpace(rating), 64)
	set("rating", m.Rating > 0)
	m.Genres = n.Genre
	set("genres", len(m.Genres) > 0)
	m.Tags = n.Tag
	set("tags", len(m.Tags) > 0)
	for _, a := range n.Actors {
		if s := strings.TrimSpace(a.Name); s != "" {
			m.Actors = append(m.Actors, s)
		}
	}
	set("actors", len(m.Actors) > 0)
	if len(lm.Fields) == 0 {
		return false
	}
	lm.Source = "nfo"
	return true
}

func parseSidecar(path string, lm *LocalMeta) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var s sidecar
	if err := json.Unmarshal(raw, &s); err != nil {
		return
	}
	m := &lm.Meta
	add := func(field string, ok bool) {
		if ok {
			lm.Fields = append(lm.Fields, field)
		}
	}
	m.Title = strings.TrimSpace(s.Title)
	add("title", m.Title != "")
	m.Overview = s.Overview
	add("overview", s.Overview != "")
	m.ReleaseDate = s.Date
	add("releaseDate", s.Date != "")
	m.Year = s.Year
	if m.Year == 0 && len(s.Date) >= 4 {
		m.Year, _ = strconv.Atoi(s.Date[:4])
	}
	add("year", m.Year > 0)
	m.Series, m.Studio, m.Director, m.Rating = s.Series, s.Studio, s.Director, s.Rating
	add("series", s.Series != "")
	add("studio", s.Studio != "")
	add("director", s.Director != "")
	add("rating", s.Rating > 0)
	m.Genres, m.Tags, m.Actors = s.Genres, s.Tags, s.Actors
	add("genres", len(s.Genres) > 0)
	add("tags", len(s.Tags) > 0)
	add("actors", len(s.Actors) > 0)
	if len(lm.Fields) > 0 {
		lm.Source = "json"
	}
}
