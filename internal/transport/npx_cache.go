package transport

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// npx 会把临时安装的包放在 <npm cache>/_npx/<hash>/。安装过程被打断（断电、重启、断网）时，
// 该目录可能只剩部分文件（例如某个依赖缺少 package.json），之后每次启动都会以
// "Cannot find module / ERR_MODULE_NOT_FOUND" 失败，且 npx 不会自动重装。
var (
	npxCacheDirPattern  = regexp.MustCompile(`(/[^\s'"<>()]*?/_npx/[0-9a-fA-F]{6,64})/node_modules/`)
	npxMissingModuleErr = regexp.MustCompile(`ERR_MODULE_NOT_FOUND|MODULE_NOT_FOUND|Cannot find (module|package)|ERR_PACKAGE_PATH_NOT_EXPORTED|ENOENT[^\n]*package\.json`)
)

// corruptNpxCacheDir 从子进程 stderr 中识别损坏的 npx 缓存目录；不是该类错误时返回空串。
func corruptNpxCacheDir(stderr string) string {
	if !npxMissingModuleErr.MatchString(stderr) {
		return ""
	}
	m := npxCacheDirPattern.FindStringSubmatch(stderr)
	if m == nil {
		return ""
	}
	// file:///root/... 的 URL 形式会带出多余的前导斜杠。
	return "/" + strings.TrimLeft(m[1], "/")
}

// removeNpxCacheDir 删除一个 npx 临时安装目录。只接受 .../_npx/<十六进制哈希> 形式的绝对路径，
// 防止误删其他目录。
func removeNpxCacheDir(dir string) error {
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || filepath.Base(filepath.Dir(clean)) != "_npx" {
		return errors.New("不是 npx 缓存目录")
	}
	if !regexp.MustCompile(`^[0-9a-fA-F]{6,64}$`).MatchString(filepath.Base(clean)) {
		return errors.New("不是 npx 缓存目录")
	}
	if _, err := os.Stat(clean); err != nil {
		return err
	}
	return os.RemoveAll(clean)
}
