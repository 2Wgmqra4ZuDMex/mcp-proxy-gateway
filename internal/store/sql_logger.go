package store

import (
	"log"
	"os"
	"time"

	"gorm.io/gorm/logger"
)

// newSQLLogger 与 GORM 默认日志器行为一致（Warn 级别、慢查询 200ms），但只输出参数占位符：
// 上游凭证、API Key 明文与代理密码都会作为 SQL 参数写入，出错或慢查询时不得原样打进日志。
func newSQLLogger() logger.Interface {
	return logger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: false,
		Colorful:                  true,
		ParameterizedQueries:      true,
	})
}
