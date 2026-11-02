package transport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCorruptNpxCacheDir(t *testing.T) {
	real := `node:internal/modules/esm/resolve:283 throw new ERR_MODULE_NOT_FOUND(...) at ModuleJob.link (node:internal/modules/esm/module_job:253:17) { code: 'ERR_MODULE_NOT_FOUND', url: 'file:///root/.npm/_npx/c34b0aca2a0f246a/node_modules/hono/ws' } Node.js v24.19.0`
	if got := corruptNpxCacheDir(real); got != "/root/.npm/_npx/c34b0aca2a0f246a" {
		t.Fatalf("应识别出损坏的 npx 缓存目录，got %q", got)
	}
	cjs := "Error: Cannot find module '/home/app/.npm/_npx/0123abcd/node_modules/zod/index.js'"
	if got := corruptNpxCacheDir(cjs); got != "/home/app/.npm/_npx/0123abcd" {
		t.Fatalf("CommonJS 报错也应识别，got %q", got)
	}
	for _, other := range []string{
		"Error: invalid API key",
		"ERR_MODULE_NOT_FOUND: Cannot find package 'x' imported from /app/server.js",
		"at /root/.npm/_npx/c34b0aca2a0f246a/node_modules/x/index.js: TypeError: boom",
	} {
		if got := corruptNpxCacheDir(other); got != "" {
			t.Fatalf("不是 npx 缓存损坏时不应识别：%q → %q", other, got)
		}
	}
}

func TestRemoveNpxCacheDirOnlyRemovesNpxHashDirs(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, ".npm", "_npx", "c34b0aca2a0f246a")
	if err := os.MkdirAll(filepath.Join(good, "node_modules", "hono"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeNpxCacheDir(good); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Fatal("损坏的 npx 缓存目录应被删除")
	}
	for _, bad := range []string{
		filepath.Join(root, ".npm"),
		filepath.Join(root, ".npm", "_npx"),
		filepath.Join(root, "other", "c34b0aca2a0f246a"),
		filepath.Join(root, ".npm", "_npx", "not-a-hash"),
		"relative/_npx/abcdef12",
	} {
		_ = os.MkdirAll(bad, 0o755)
		if err := removeNpxCacheDir(bad); err == nil {
			t.Fatalf("不应删除非 npx 缓存目录：%s", bad)
		}
	}
}
