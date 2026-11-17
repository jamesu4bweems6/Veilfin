package scraper

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MetaTube 对接自行部署的 MetaTube 服务端（https://github.com/metatube-community/metatube-sdk-go），
// 通过它查询日本影片的元数据。接口：
//
//	GET /v1/movies/search?q=番号        → {"data":[{provider,id,number,...}]}
//	GET /v1/movies/{provider}/{id}      → {"data":{...影片信息}}
//	GET /v1/images/primary/{provider}/{id}   竖版海报（服务端裁剪）
//	GET /v1/images/backdrop/{provider}/{id}  横版背景
type MetaTube struct {
	Base  string
	Token string
	H     *HTTP
}

func (m *MetaTube) Name() string { return "metatube" }

func (m *MetaTube) header() http.Header {
	h := http.Header{}
	if m.Token != "" {
		h.Set("Authorization", "Bearer "+m.Token)
	}
	return h
}

type mtSearchResult struct {
	ID       string  `json:"id"`
	Provider string  `json:"provider"`
	Number   string  `json:"number"`
	Title    string  `json:"title"`
	Score    float64 `json:"score"`
}

type mtMovie struct {
	ID          string   `json:"id"`
	Number      string   `json:"number"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Provider    string   `json:"provider"`
	Director    string   `json:"director"`
	Actors      []string `json:"actors"`
	Maker       string   `json:"maker"`
	Label       string   `json:"label"`
	Series      string   `json:"series"`
	Genres      []string `json:"genres"`
	Score       float64  `json:"score"`
	ReleaseDate string   `json:"release_date"`
	CoverURL    string   `json:"cover_url"`
	BigCoverURL string   `json:"big_cover_url"`
}

func (m *MetaTube) Lookup(ctx context.Context, q Query) (*Result, error) {
	if m.Base == "" {
		return nil, errors.New("未配置 MetaTube 服务地址")
	}
	if q.Code == "" {
		return nil, ErrNoMatch
	}
	base := strings.TrimRight(m.Base, "/")
	var sr struct {
		Data []mtSearchResult `json:"data"`
	}
	if err := m.H.getJSON(ctx, base+"/v1/movies/search?fallback=true&q="+url.QueryEscape(q.Code), m.header(), &sr); err != nil {
		return nil, err
	}
	want := NormalizeCode(q.Code)
	var pick *mtSearchResult
	for i := range sr.Data {
		if NormalizeCode(sr.Data[i].Number) == want {
			pick = &sr.Data[i]
			break
		}
	}
	if pick == nil {
		return nil, ErrNoMatch
	}
	var mr struct {
		Data mtMovie `json:"data"`
	}
	path := "/" + url.PathEscape(pick.Provider) + "/" + url.PathEscape(pick.ID)
	if err := m.H.getJSON(ctx, base+"/v1/movies"+path, m.header(), &mr); err != nil {
		return nil, err
	}
	mv := mr.Data
	date := mv.ReleaseDate
	if t, err := time.Parse(time.RFC3339, date); err == nil {
		date = t.Format("2006-01-02")
	} else if len(date) > 10 {
		date = date[:10]
	}
	studio := mv.Maker
	if studio == "" {
		studio = mv.Label
	}
	return &Result{
		Source:      "metatube:" + mv.Provider,
		Title:       mv.Title,
		Overview:    mv.Summary,
		ReleaseDate: date,
		Year:        yearOf(date),
		Code:        strings.ToUpper(mv.Number),
		Studio:      studio,
		Director:    mv.Director,
		Series:      mv.Series,
		Rating:      mv.Score,
		Genres:      mv.Genres,
		Actors:      mv.Actors,
		PosterURL:   base + "/v1/images/primary" + path,
		BackdropURL: base + "/v1/images/backdrop" + path,
	}, nil
}

func (m *MetaTube) Fetch(ctx context.Context, u string) ([]byte, error) {
	h := http.Header{}
	if strings.HasPrefix(u, strings.TrimRight(m.Base, "/")) {
		h = m.header()
	}
	return m.H.get(ctx, u, h)
}
