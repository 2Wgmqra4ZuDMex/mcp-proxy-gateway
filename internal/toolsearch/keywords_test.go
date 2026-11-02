package toolsearch

import "testing"

func TestSearchMatchesChineseThroughAIKeywords(t *testing.T) {
	docs := []Doc{
		{Name: "firecrawl_search", Description: "Search the web and return ranked results."},
		{Name: "firecrawl_scrape", Description: "Scrape one URL and return its content.", Keywords: "抓取单个网页内容 网页抓取 爬虫 scrape 读取网页"},
		{Name: "query-docs", Description: "Query documentation for a library.", Keywords: "编程库文档 API 用法 最新版本 react"},
	}
	ix := Build(docs)

	got := ix.Search("抓取网页内容", 3, 0)
	if got.Total == 0 || docs[got.Hits[0].DocIndex].Name != "firecrawl_scrape" {
		t.Fatalf("中文查询应通过 AI 关键词命中 scrape：%+v", got)
	}
	got = ix.Search("react 怎么用", 3, 0)
	if got.Total == 0 || docs[got.Hits[0].DocIndex].Name != "query-docs" {
		t.Fatalf("应命中编程文档工具：%+v", got)
	}
	without := Build([]Doc{{Name: "firecrawl_scrape", Description: "Scrape one URL and return its content."}})
	if res := without.Search("抓取网页内容", 3, 0); res.Total == 0 {
		t.Fatalf("常用中文词内置了英文对照，没有 AI 关键词也应命中：%+v", res)
	}
	if res := without.Search("整理报销单", 3, 0); res.Total != 0 {
		t.Fatalf("无关的中文查询不应命中英文描述：%+v", res)
	}
}
