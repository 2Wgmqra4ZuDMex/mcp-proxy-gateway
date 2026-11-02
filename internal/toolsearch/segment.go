package toolsearch

import (
	"bufio"
	_ "embed"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// zhCommonDict 为常用中文词表（词 词频 词性），取自 gse 简体词典词频最高的 8 万个
// 2～6 字词（gse 以 Apache-2.0 发布，见 dict/NOTICE）。只用作分词词表。
//
//go:embed dict/zh_common.txt
var zhCommonDict string

const (
	maxWordRunes = 6
	// extraWordFreq 为运行时补充词（AI 生成的中文关键词）的词频，接近常用词，
	// 让领域词（如"网页抓取""剩余额度"）在切分时优先成词。
	extraWordFreq = 20000
)

type segmenter struct {
	freq    map[string]float64
	logTot  float64
	minLogP float64
}

var (
	baseSegOnce sync.Once
	baseSeg     *segmenter
)

func baseSegmenter() *segmenter {
	baseSegOnce.Do(func() { baseSeg = loadSegmenter() })
	return baseSeg
}

func loadSegmenter() *segmenter {
	{
		seg := &segmenter{freq: make(map[string]float64, 80000)}
		total := 0.0
		scanner := bufio.NewScanner(strings.NewReader(zhCommonDict))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			f, err := strconv.ParseFloat(fields[1], 64)
			if err != nil || f <= 0 {
				continue
			}
			seg.freq[fields[0]] = f
			total += f
		}
		if total <= 0 {
			total = 1
		}
		seg.logTot = math.Log(total)
		// 未登录单字的概率取一个很小的值，使已知词优先。
		seg.minLogP = math.Log(1) - seg.logTot - 2
		return seg
	}
}

func (s *segmenter) logProb(word string, extra map[string]struct{}) (float64, bool) {
	if f, ok := s.freq[word]; ok {
		if _, boosted := extra[word]; boosted && f < extraWordFreq {
			f = extraWordFreq
		}
		return math.Log(f) - s.logTot, true
	}
	if _, ok := extra[word]; ok {
		return math.Log(extraWordFreq) - s.logTot, true
	}
	return 0, false
}

func (s *segmenter) known(word string, extra map[string]struct{}) bool {
	if _, ok := s.freq[word]; ok {
		return true
	}
	_, ok := extra[word]
	return ok
}

// cut 用"词表 + 词频"的最大概率路径切分一段连续汉字（与 jieba 的无 HMM 模式相同）。
func (s *segmenter) cut(runes []rune, extra map[string]struct{}) []string {
	n := len(runes)
	if n == 0 {
		return nil
	}
	best := make([]float64, n+1)
	next := make([]int, n+1)
	for i := n - 1; i >= 0; i-- {
		best[i] = math.Inf(-1)
		for j := i + 1; j <= n && j-i <= maxWordRunes; j++ {
			lp, ok := s.logProb(string(runes[i:j]), extra)
			if !ok {
				if j != i+1 {
					continue
				}
				lp = s.minLogP
			}
			if v := lp + best[j]; v > best[i] {
				best[i], next[i] = v, j
			}
		}
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i = next[i] {
		out = append(out, string(runes[i:next[i]]))
	}
	return out
}

// hanTokens 切分一段连续汉字：输出切出的词；三字及以上的词再补上其中的已知子词，
// 让"网页抓取"也能被"网页""抓取"命中。
func hanTokens(runes []rune, extra map[string]struct{}) []string {
	seg := baseSegmenter()
	words := seg.cut(runes, extra)
	out := make([]string, 0, len(words)*2)
	for _, w := range words {
		out = append(out, w)
		wr := []rune(w)
		if len(wr) < 3 {
			continue
		}
		for size := len(wr) - 1; size >= 2; size-- {
			for i := 0; i+size <= len(wr); i++ {
				sub := string(wr[i : i+size])
				if seg.known(sub, extra) {
					out = append(out, sub)
				}
			}
		}
	}
	return out
}

// isHanWord 报告 s 是否全部由汉字组成且长度在 [2, maxWordRunes] 内。
func isHanWord(s string) bool {
	n := utf8.RuneCountInString(s)
	if n < 2 || n > maxWordRunes {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}

// looseHanPhraseMatch 判断汉字短语是否以"相邻字之间最多隔一个字"的方式出现在查询里，
// 例如关键词"截图"能匹配查询"截个图"。
func looseHanPhraseMatch(query []rune, phrase []rune) bool {
	if len(phrase) < 2 || len(phrase) > 4 {
		return false
	}
	for start := range query {
		if query[start] != phrase[0] {
			continue
		}
		pos, ok := start, true
		for _, r := range phrase[1:] {
			found := false
			for gap := 1; gap <= 2 && pos+gap < len(query); gap++ {
				if query[pos+gap] == r {
					pos += gap
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
