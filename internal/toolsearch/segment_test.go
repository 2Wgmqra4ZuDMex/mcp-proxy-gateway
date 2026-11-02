package toolsearch

import (
	"reflect"
	"runtime"
	"testing"
)

func TestSegmenterSplitsCommonChineseQueries(t *testing.T) {
	cases := map[string][]string{
		"附近的餐厅": {"附近", "的", "餐厅"},
		"整个网站":  {"整个", "网站"},
		"剩余额度":  {"剩余", "额度"},
	}
	for input, want := range cases {
		if got := Tokenize(input); !reflect.DeepEqual(got, want) {
			t.Errorf("Tokenize(%q)=%v, want %v", input, got, want)
		}
	}
	extra := map[string]struct{}{"网页抓取": {}}
	if got := tokenizeWith("网页抓取", 0, extra); !reflect.DeepEqual(got, []string{"网页抓取", "网页", "抓取"}) {
		t.Errorf("补充词应整体成词并补上子词，got %v", got)
	}
	if got := tokenizeWith("爬取整个网站", 0, map[string]struct{}{"爬取": {}}); !reflect.DeepEqual(got, []string{"爬取", "整个", "网站"}) {
		t.Errorf("补充词应整体成词并补上子词，got %v", got)
	}
}

func TestLooseHanPhraseMatch(t *testing.T) {
	if !looseHanPhraseMatch([]rune("给网页截个图"), []rune("截图")) {
		t.Fatal("截图 应匹配 截个图")
	}
	if looseHanPhraseMatch([]rune("截了一张图"), []rune("截图")) {
		t.Fatal("间隔超过一个字不应匹配")
	}
}

func TestSegmenterMemoryIsSmall(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	seg := loadSegmenter()
	runtime.GC()
	runtime.ReadMemStats(&after)
	mb := float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / 1e6
	t.Logf("词表内存约 %.1f MB（%d 个词）", mb, len(seg.freq))
	if mb > 25 {
		t.Fatalf("词表内存过大：%.1f MB", mb)
	}
}

func TestSearchChineseRanksIntendedToolFirst(t *testing.T) {
	ix := Build([]Doc{
		{Name: "brave_local_search", Description: "Search local businesses", KeywordPhrases: []string{"附近商家", "餐厅", "营业时间", "附近的"}, Keywords: "搜索本地商家和地点"},
		{Name: "credit_usage", Description: "Check remaining credits", KeywordPhrases: []string{"剩余额度", "积分余额", "额度查询"}, Keywords: "查询团队当前的积分余额"},
		{Name: "scrape", Description: "Scrape one URL", KeywordPhrases: []string{"网页抓取", "截图", "网页内容"}, Keywords: "抓取单个网页"},
		{Name: "noise", Description: "Generic tool", KeywordPhrases: []string{"通用工具"}, Keywords: "一个通用的工具，用于各种用途"},
	})
	cases := map[string]string{
		"附近的餐厅":      "brave_local_search",
		"还剩多少额度":     "credit_usage",
		"给网页截个图":     "scrape",
		"读一下这个网页的内容": "scrape",
	}
	for query, want := range cases {
		res := ix.Search(query, 5, 0)
		if res.Total == 0 || ix.docs[res.Hits[0].DocIndex].Name != want {
			names := []string{}
			for _, h := range res.Hits {
				names = append(names, ix.docs[h.DocIndex].Name)
			}
			t.Errorf("Search(%q) 第一名应为 %s，got %v", query, want, names)
		}
		for _, h := range res.Hits {
			if ix.docs[h.DocIndex].Name == "noise" && h.Covered > 0 {
				t.Errorf("Search(%q) 不应让无关工具靠单字/停用词得到覆盖：%+v", query, h)
			}
		}
	}
}
