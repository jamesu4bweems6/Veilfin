package scraper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// TMDB 对接 The Movie Database v3 API。Key 可以是 v3 API Key，也可以是 v4 读访问令牌（以 eyJ 开头）。
type TMDB struct {
	Key      string
	Language string // 默认 zh-CN
	Base     string // 默认 https://api.themoviedb.org/3，测试时可替换
	ImgBase  string // 默认 https://image.tmdb.org/t/p
	H        *HTTP
}

func (t *TMDB) Name() string { return "tmdb" }

func (t *TMDB) api(path string, params url.Values) (string, http.Header) {
	base := t.Base
	if base == "" {
		base = "https://api.themoviedb.org/3"
	}
	if params == nil {
		params = url.Values{}
	}
	lang := t.Language
	if lang == "" {
		lang = "zh-CN"
	}
	params.Set("language", lang)
	h := http.Header{}
	if strings.HasPrefix(t.Key, "eyJ") {
		h.Set("Authorization", "Bearer "+t.Key)
	} else {
		params.Set("api_key", t.Key)
	}
	return strings.TrimRight(base, "/") + path + "?" + params.Encode(), h
}

func (t *TMDB) img(size, p string) string {
	if p == "" {
		return ""
	}
	base := t.ImgBase
	if base == "" {
		base = "https://image.tmdb.org/t/p"
	}
	return strings.TrimRight(base, "/") + "/" + size + p
}

func (t *TMDB) Lookup(ctx context.Context, q Query) (*Result, error) {
	if t.Key == "" {
		return nil, errors.New("未配置 TMDB API Key")
	}
	if q.Title == "" {
		return nil, ErrNoMatch
	}
	params := url.Values{"query": {q.Title}, "include_adult": {"false"}}
	if q.Year > 0 {
		params.Set("year", fmt.Sprint(q.Year))
	}
	u, h := t.api("/search/movie", params)
	var sr struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := t.H.getJSON(ctx, u, h, &sr); err != nil {
		return nil, err
	}
	if len(sr.Results) == 0 && q.Year > 0 { // 年份可能不准，去掉年份再试一次
		params.Del("year")
		u, h = t.api("/search/movie", params)
		if err := t.H.getJSON(ctx, u, h, &sr); err != nil {
			return nil, err
		}
	}
	if len(sr.Results) == 0 {
		return nil, ErrNoMatch
	}
	u, h = t.api(fmt.Sprintf("/movie/%d", sr.Results[0].ID), url.Values{"append_to_response": {"credits"}})
	var mv struct {
		Title         string  `json:"title"`
		OriginalTitle string  `json:"original_title"`
		Overview      string  `json:"overview"`
		ReleaseDate   string  `json:"release_date"`
		PosterPath    string  `json:"poster_path"`
		BackdropPath  string  `json:"backdrop_path"`
		VoteAverage   float64 `json:"vote_average"`
		Genres        []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Companies []struct {
			Name string `json:"name"`
		} `json:"production_companies"`
		Collection *struct {
			Name string `json:"name"`
		} `json:"belongs_to_collection"`
		Credits struct {
			Cast []struct {
				Name string `json:"name"`
			} `json:"cast"`
			Crew []struct {
				Name string `json:"name"`
				Job  string `json:"job"`
			} `json:"crew"`
		} `json:"credits"`
	}
	if err := t.H.getJSON(ctx, u, h, &mv); err != nil {
		return nil, err
	}
	r := &Result{
		Source:        "tmdb",
		Title:         mv.Title,
		OriginalTitle: mv.OriginalTitle,
		Overview:      mv.Overview,
		ReleaseDate:   mv.ReleaseDate,
		Year:          yearOf(mv.ReleaseDate),
		Rating:        mv.VoteAverage,
		PosterURL:     t.img("w780", mv.PosterPath),
		BackdropURL:   t.img("w1280", mv.BackdropPath),
	}
	if mv.OriginalTitle == mv.Title {
		r.OriginalTitle = ""
	}
	for _, g := range mv.Genres {
		r.Genres = append(r.Genres, g.Name)
	}
	if len(mv.Companies) > 0 {
		r.Studio = mv.Companies[0].Name
	}
	if mv.Collection != nil {
		r.Series = mv.Collection.Name
	}
	for i, c := range mv.Credits.Cast {
		if i >= 15 {
			break
		}
		r.Actors = append(r.Actors, c.Name)
	}
	for _, c := range mv.Credits.Crew {
		if c.Job == "Director" {
			r.Director = c.Name
			break
		}
	}
	return r, nil
}

func (t *TMDB) Fetch(ctx context.Context, u string) ([]byte, error) { return t.H.get(ctx, u, nil) }
