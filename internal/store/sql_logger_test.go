package store

import (
	"testing"

	"gorm.io/gorm"
)

func TestSQLLoggerHidesParameters(t *testing.T) {
	filter, ok := newSQLLogger().(gorm.ParamsFilter)
	if !ok {
		t.Fatal("SQL 日志器应实现参数过滤")
	}
	sql, params := filter.ParamsFilter(t.Context(), "INSERT INTO outbound_proxy (url) VALUES ($1)", "socks5://u:secret@10.0.0.1:1080")
	if params != nil {
		t.Fatalf("日志不应携带 SQL 参数值，got %v（sql=%s）", params, sql)
	}
}
