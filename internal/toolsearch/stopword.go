package toolsearch

// stopwords intentionally only contains grammatical words. Tool verbs such as
// list, get, set, and run must remain searchable.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "of": {}, "to": {}, "in": {}, "on": {},
	"at": {}, "for": {}, "with": {}, "and": {}, "or": {}, "is": {}, "are": {},
	"be": {}, "i": {}, "me": {}, "my": {}, "please": {},
	"的": {}, "了": {}, "我": {}, "你": {}, "帮": {}, "请": {}, "一": {}, "个": {},
	"把": {}, "给": {}, "帮我": {}, "一个": {}, "请问": {}, "怎么": {}, "如何": {}, "可以": {},
	"一下": {}, "这个": {}, "那个": {}, "这些": {}, "那些": {}, "什么": {}, "怎样": {}, "哪些": {},
	"有": {}, "是": {}, "在": {}, "和": {}, "与": {}, "及": {}, "或": {}, "吗": {}, "呢": {}, "吧": {},
	"啊": {}, "要": {}, "想": {}, "能": {}, "用": {}, "去": {}, "来": {}, "让": {}, "被": {}, "都": {},
	"也": {}, "就": {}, "还": {}, "再": {}, "些": {}, "几": {}, "里": {}, "上": {}, "下": {}, "中": {},
	"很": {}, "最": {}, "已": {}, "个人": {}, "我们": {}, "你们": {}, "他们": {}, "需要": {}, "看看": {},
	"有没有": {}, "怎么样": {}, "是否": {}, "一些": {}, "所有": {}, "整个": {},
}

func removeStopwords(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := stopwords[token]; !ok {
			out = append(out, token)
		}
	}
	return out
}
