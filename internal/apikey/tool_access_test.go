package apikey

import (
	"context"
	"strings"
	"testing"

	"github.com/myGithub/mcp-proxy-gateway/internal/store"
)

type memoryToolAccessRepo struct {
	mode  string
	names []string
}

func (r *memoryToolAccessRepo) Get(context.Context, string) (store.APIKeyToolAccess, error) {
	mode := r.mode
	if mode == "" {
		mode = "all"
	}
	return store.APIKeyToolAccess{Mode: mode, ToolNames: append([]string(nil), r.names...)}, nil
}

func (r *memoryToolAccessRepo) Replace(_ context.Context, _ string, mode string, names []string) error {
	r.mode = mode
	r.names = append([]string(nil), names...)
	return nil
}

func TestToolAccessManagerSelectedNormalizesNames(t *testing.T) {
	repo := &memoryToolAccessRepo{}
	m := NewToolAccessManager(repo)

	cfg, err := m.Set(context.Background(), "key-1", ToolAccessConfig{
		Mode:      UpstreamAccessSelected,
		ToolNames: []string{" tavily_search ", "web_search_exa", "tavily_search", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.ToolNames, ",") != "tavily_search,web_search_exa" {
		t.Fatalf("工具名应去空白、去重并排序，实际 %v", cfg.ToolNames)
	}
	allowed, restricted, err := m.AllowedToolNames(context.Background(), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if !restricted || len(allowed) != 2 {
		t.Fatalf("selected 模式应限制为 2 个工具，restricted=%v allowed=%v", restricted, allowed)
	}
}

func TestToolAccessManagerAllClearsNames(t *testing.T) {
	repo := &memoryToolAccessRepo{mode: "selected", names: []string{"a"}}
	m := NewToolAccessManager(repo)

	cfg, err := m.Set(context.Background(), "key-1", ToolAccessConfig{Mode: UpstreamAccessAll, ToolNames: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != UpstreamAccessAll || len(cfg.ToolNames) != 0 {
		t.Fatalf("all 模式应清空白名单，实际 %+v", cfg)
	}
	if _, restricted, _ := m.AllowedToolNames(context.Background(), "key-1"); restricted {
		t.Fatal("all 模式不应限制")
	}
}

func TestToolAccessManagerRejectsInvalidMode(t *testing.T) {
	m := NewToolAccessManager(&memoryToolAccessRepo{})
	if _, err := m.Set(context.Background(), "key-1", ToolAccessConfig{Mode: "bogus"}); err == nil {
		t.Fatal("非法模式应校验失败")
	}
}

func TestToolAccessManagerEmptyAPIKeyUnrestricted(t *testing.T) {
	m := NewToolAccessManager(&memoryToolAccessRepo{mode: "selected"})
	if _, restricted, err := m.AllowedToolNames(context.Background(), ""); err != nil || restricted {
		t.Fatalf("无 API Key 视角不应限制，restricted=%v err=%v", restricted, err)
	}
}
