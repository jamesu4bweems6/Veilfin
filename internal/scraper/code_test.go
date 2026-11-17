package scraper

import "testing"

func TestParseCode(t *testing.T) {
	cases := []struct {
		in   string
		code string
		sub  bool
		part int
	}{
		{"ABC-123.mp4", "ABC-123", false, 0},
		{"abc123.mp4", "ABC-123", false, 0},
		{"[thz.la]SSIS-001-C.mp4", "SSIS-001", true, 0},
		{"www.example.com@IPX-456.mp4", "IPX-456", false, 0},
		{"MIDV-018-UC.mp4", "MIDV-018", true, 0},
		{"STARS-123-cd2.mp4", "STARS-123", false, 2},
		{"PRED-100-A.mp4", "PRED-100", false, 1},
		{"FC2-PPV-1234567.mp4", "FC2-PPV-1234567", false, 0},
		{"fc2ppv_1234567.mp4", "FC2-PPV-1234567", false, 0},
		{"HEYZO-1234.mp4", "HEYZO-1234", false, 0},
		{"123456-789-carib.mp4", "123456-789", false, 0},
		{"010120_001.mp4", "010120_001", false, 0},
		{"abp00123.mp4", "ABP-123", false, 0},
		{"ABP-123 1080p x264.mp4", "ABP-123", false, 0},
		{"1080p.HEVC.MP4-SSNI-777.mp4", "SSNI-777", false, 0},
		{"家庭录像 2023.mp4", "", false, 0},
		{"VID_20230101_120000.mp4", "", false, 0},
	}
	for _, c := range cases {
		got := ParseCode(c.in)
		if got.Code != c.code || got.Subtitle != c.sub || got.Part != c.part {
			t.Errorf("%s: 得到 %+v，期望 code=%s sub=%v part=%d", c.in, got, c.code, c.sub, c.part)
		}
	}
}

func TestNormalizeCode(t *testing.T) {
	if NormalizeCode("abp-0123") != NormalizeCode("ABP123") {
		t.Fatal("规范化后应相等")
	}
	if NormalizeCode("FC2-PPV-1234567") != "FC2PPV1234567" {
		t.Fatalf("FC2 规范化错误: %s", NormalizeCode("FC2-PPV-1234567"))
	}
}

func TestParseTitle(t *testing.T) {
	cases := []struct {
		in    string
		title string
		year  int
	}{
		{"The.Matrix.1999.1080p.BluRay.x264.mp4", "The Matrix", 1999},
		{"[字幕组] 千与千寻 (2001).mp4", "千与千寻", 2001},
		{"Inception_2010_4K.mp4", "Inception", 2010},
		{"家庭聚会.mp4", "家庭聚会", 0},
	}
	for _, c := range cases {
		title, year := ParseTitle(c.in)
		if title != c.title || year != c.year {
			t.Errorf("%s: 得到 %q %d，期望 %q %d", c.in, title, year, c.title, c.year)
		}
	}
}
