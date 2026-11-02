package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/myGithub/mcp-proxy-gateway/internal/config"
	"github.com/myGithub/mcp-proxy-gateway/internal/safego"
)

var ErrRestart = errors.New("restart requested")

// Run 先做启动连通性探测（Req 20.1），随后启动后台服务与 HTTP 服务对外提供服务，
// 并在 ctx 取消时优雅停机（Req 20.1 的「先探测再对外服务」顺序）。
//
// 探测仅记录结构化日志，不因单项依赖失败而拒绝启动——是否对外服务由运维据日志判断；
// 但若 PG/Redis 等关键依赖在 New 阶段就连接失败，启动早已终止（见 New）。
func (a *App) Run(ctx context.Context) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	// 0) 首启应用 YAML 中配置的日志级别（LevelVar 初始为 Info，此处校正为实际配置值）。
	if a.levelVar != nil && a.cfg != nil {
		a.levelVar.Set(slogLevel(a.cfg.Config().Server.LogLevel))
	}

	// 1) 启动连通性探测：按序记录 PG/Redis/各启用上游/小智的连通性（Req 20.1-20.5）。
	report := a.prober.ProbeStartup(runCtx)
	if report.AllOK() {
		a.logger.Info("启动连通性探测全部通过", "checks", len(report.Results))
	} else {
		a.logger.Warn("启动连通性探测存在失败项，仍继续对外服务", "failures", len(report.Failures()))
	}

	// 2) 启动后台服务（统计 worker/清理、审计清理、同步 cron、小智连接）。
	a.startBackground(runCtx)

	// 3) 启动 HTTP 服务并等待 ctx 取消后优雅停机。
	servers := a.httpServers()
	errCh := make(chan error, len(servers))
	for _, spec := range servers {
		go func(spec httpServerSpec) {
			defer func() {
				if recovered := recover(); recovered != nil {
					safego.LogRecovered(a.logger, "HTTP 服务 worker panic 已恢复", recovered, "name", spec.name, "addr", spec.server.Addr)
					errCh <- fmt.Errorf("%s HTTP 服务异常退出：%v", spec.name, recovered)
				}
			}()
			a.logger.Info("HTTP 服务开始监听", "name", spec.name, "addr", spec.server.Addr)
			if err := spec.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s HTTP 服务异常退出：%w", spec.name, err)
				return
			}
			errCh <- nil
		}(spec)
	}

	select {
	case <-ctx.Done():
		a.logger.Info("收到停机信号，开始优雅停机", "cause", shutdownCause(ctx))
		cancelRun()
		a.shutdown()
		return nil
	case <-a.restartCh:
		a.logger.Info("restart requested, shutting down current app")
		cancelRun()
		a.shutdown()
		return ErrRestart
	case err := <-errCh:
		// HTTP 服务异常退出：停止后台服务并清理。
		cancelRun()
		a.shutdown()
		return err
	}
}

// shutdownCause 描述触发停机的原因，用于停机日志。
//
// Go 1.26 起 signal.NotifyContext 改用 context.WithCancelCause 取消上下文，收到信号时
// 会把信号写入 cause，于是这里能区分「容器编排发来的 SIGTERM」与「人工 Ctrl+C 的 SIGINT」
// ——两者的排查方向完全不同。非信号路径（调用 stop 或父上下文结束）的 cause 为
// context.Canceled，此时按上下文取消描述。
func shutdownCause(ctx context.Context) string {
	cause := context.Cause(ctx)
	switch {
	case cause == nil:
		return "未知"
	case errors.Is(cause, context.Canceled):
		return "上下文取消"
	default:
		return cause.Error()
	}
}

type httpServerSpec struct {
	name   string
	server *http.Server
}

// header 数量上限（Go 1.27 的 Server.MaxHeaderValueCount）。
//
// 已有的 MaxHeaderBytes 限制的是请求头总字节数，挡不住「大量极小 header」——几千个
// 单字节头可以在总字节数很小的情况下把服务端解析出的 map 撑大。该字段补上这个缺口。
//
// 按 header 行计数（同一行内的逗号分隔多值只算一次）。标准库默认 500；这里按实际用量
// 收紧，两个取值都远高于正常请求所需，只用于挡异常流量：
//   - 对外 MCP 面向公网，MCP 请求头很少（鉴权、内容协商、少量链路追踪头）；
//   - 管理端口走浏览器，请求头稍多，且需给反向代理注入的 X-Forwarded-* 等留足余量。
//
// 排查提示：越限时 net/http 直接在连接上回 431 并关闭，请求不进入 handler，因此网关侧不会
// 留下访问日志或审计记录。若客户端报 431 而网关日志为空，应先怀疑代理链注入的头行数超过了
// 这里的上限。
const (
	publicMCPMaxHeaderValueCount = 64
	adminMaxHeaderValueCount     = 128
)

func (a *App) httpServers() []httpServerSpec {
	a.adminServer = &http.Server{
		Addr:                a.adminAddr,
		Handler:             a.adminEngine,
		ReadHeaderTimeout:   10 * time.Second,
		IdleTimeout:         120 * time.Second,
		MaxHeaderValueCount: adminMaxHeaderValueCount,
	}
	servers := []httpServerSpec{{name: "管理", server: a.adminServer}}

	if a.publicMCPAddr != "" && a.publicMCPEngine != nil {
		a.publicMCPServer = &http.Server{
			Addr:                a.publicMCPAddr,
			Handler:             a.publicMCPEngine,
			ReadHeaderTimeout:   5 * time.Second,
			IdleTimeout:         60 * time.Second,
			MaxHeaderBytes:      16 << 10,
			MaxHeaderValueCount: publicMCPMaxHeaderValueCount,
		}
		servers = append(servers, httpServerSpec{name: "对外 MCP", server: a.publicMCPServer})
	}
	return servers
}

// startBackground 启动各后台服务（Req 7、16）。
func (a *App) startBackground(ctx context.Context) {
	// 恢复数据库中已有上游的连接状态机登记，确保进程重启后可直接重连与展示状态。
	a.runBackgroundStep("恢复上游连接状态", func() {
		if err := a.mgr.RestoreConnections(ctx); err != nil {
			a.logger.Error("恢复上游连接状态失败，后续启用或重连时将按需恢复", "error", err)
		}
	})
	a.runBackgroundStep("恢复安全封禁缓存", func() {
		if a.securityGuard == nil {
			return
		}
		if err := a.securityGuard.RestoreActiveBlocks(ctx); err != nil {
			a.logger.Warn("恢复安全封禁缓存失败，后续新触发封禁仍会生效", "error", err)
		}
	})

	// 统计异步落库 worker 与保留期清理（Req 16.8、16.10）。
	a.runBackgroundStep("启动统计落库 worker", func() { a.statRecorder.Start(ctx) })
	a.runBackgroundStep("启动统计保留期清理", func() { a.statCleaner.Start(ctx) })
	// 审计异步落库 worker（登录/增删改/访问被拒旁路，Req 22）。
	a.runBackgroundStep("启动审计落库 worker", func() {
		if a.auditRecorder != nil {
			a.auditRecorder.Start(ctx)
		}
	})

	// 同步 cron 调度：注册周期同步任务并启动调度器（Req 7）。
	a.runBackgroundStep("启动周期同步调度", func() {
		syncCron := a.cfg.Config().Sync.Cron
		if err := a.scheduler.UpdateSchedule(syncCron, func() {
			runCtx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
			defer cancel()
			a.syncer.SyncEnabledAutoSync(runCtx)
		}); err != nil {
			a.logger.Error("注册同步 cron 调度失败，周期同步未启用", "cron", syncCron, "error", err)
		} else {
			a.scheduler.Start()
		}
	})

	if a.discovery != nil {
		a.discovery.Start(ctx)
	}

	// 启动后为缺失工具缓存的已启用上游补拉一次（如刚导入备份），无需等待周期同步或手动刷新。
	a.runBackgroundStep("补拉缺失的工具缓存", func() {
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					safego.LogRecovered(a.logger, "启动补拉工具缓存 panic 已恢复", recovered)
				}
			}()
			runCtx, cancel := context.WithTimeout(ctx, startupCacheFillTimeout(a.syncTimeout))
			defer cancel()
			a.syncer.EnsureCachedForEnabled(runCtx)
			// 工具缓存补齐后，为尚未生成说明的工具补生成（仅在开启工具说明增强时生效）。
			if a.discovery != nil {
				a.discovery.ToolsChanged()
			}
		}()
	})

	// 审计保留期清理：以 24 小时为周期独立运行（Req 22.5）。
	a.runBackgroundStep("启动审计保留期清理", func() { a.startAuditRetention(ctx) })
}

// startupCacheFillTimeout 为启动补拉工具缓存的整体时限：各上游并发拉取，单次同步超时
// 之外再留出连接建立的余量；同步超时未配置时取 2 分钟。
func startupCacheFillTimeout(syncTimeout time.Duration) time.Duration {
	if syncTimeout <= 0 {
		return 2 * time.Minute
	}
	return syncTimeout + time.Minute
}

func (a *App) runBackgroundStep(name string, step func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			safego.LogRecovered(a.logger, "后台服务启动步骤 panic 已恢复，继续启动其他组件", recovered, "step", name)
		}
	}()
	if step != nil {
		step()
	}
}

// ApplySettings 将已保存的 YAML 配置应用到当前运行中的进程组件。
//
// 配置落盘由 config.Manager 负责；此处只处理需要即时影响运行态的部分：
// 对外 MCP 模式影响后续新建连接，小智接入需要按新配置启停或重连，
// 日志级别经 LevelVar 即时切换（无需重启进程）。
func (a *App) ApplySettings(cfg config.YAMLConfig) error {
	// 日志级别热更新：LevelVar 绑定到 console handler，设置后即时生效。
	if a.levelVar != nil {
		newLevel := slogLevel(cfg.Server.LogLevel)
		a.levelVar.Set(newLevel)
		if a.logger != nil {
			a.logger.Info("日志级别已更新", "level", config.NormalizeLogLevel(cfg.Server.LogLevel))
		}
	}
	if a.mcpService != nil {
		a.mcpService.SetDiscoveryLimit(cfg.MCPAPI.SmartDiscoveryLimit)
	}
	if a.mcpEndpoints != nil {
		a.mcpEndpoints.SetRequestBodyLimitMiB(cfg.MCPAPI.RequestBodyLimitMiB)
		a.mcpEndpoints.SetDefaultMode(defaultMCPMode(cfg.MCPAPI.DefaultMode))
	}
	if a.agg != nil {
		a.agg.SetRoutingStrategy(cfg.Aggregation.ToolRoutingStrategy)
	}
	return nil
}

// RequestRestart asks Run to gracefully stop and let main rebuild the App.
func (a *App) RequestRestart() {
	if a.restartCh == nil {
		return
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		select {
		case a.restartCh <- struct{}{}:
		default:
		}
	}()
}

// RuntimeServerConfig returns the listener config currently effective in this process.
func (a *App) RuntimeServerConfig() config.ServerConfig {
	return config.ServerConfig{
		AdminAddr:            a.adminAddr,
		PublicMCPAddr:        a.publicMCPAddr,
		ExposeMCPOnAdminAddr: a.exposeMCPOnAdminAddr,
	}
}

// slogLevel 把配置中的日志级别字符串映射为 slog.Level；非法或空值回退到 info。
func slogLevel(s string) slog.Level {
	switch s {
	case config.LogLevelDebug:
		return slog.LevelDebug
	case config.LogLevelWarn:
		return slog.LevelWarn
	case config.LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// startAuditRetention starts audit retention cleanup.
func (a *App) startAuditRetention(ctx context.Context) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				safego.LogRecovered(a.logger, "审计保留期清理 worker panic 已恢复", recovered)
			}
		}()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		// 启动即清理一次，使重启后及时回收超期记录。
		a.runAuditRetentionCleanup(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.runAuditRetentionCleanup(ctx)
			}
		}
	}()
}

func (a *App) runAuditRetentionCleanup(ctx context.Context) {
	defer func() {
		if recovered := recover(); recovered != nil {
			safego.LogRecovered(a.logger, "审计保留期清理 panic 已恢复，等待下一轮重试", recovered)
		}
	}()
	if _, err := a.auditSvc.Cleanup(ctx); err != nil {
		a.logger.Warn("审计日志保留期清理失败", "error", err)
	}
}

// shutdown 优雅停止 HTTP 服务与全部后台服务，并释放基础设施连接。
func (a *App) shutdown() {
	// 1) 停止接收新请求：关闭 HTTP 服务，给在途请求收尾时间。
	for _, spec := range []httpServerSpec{{name: "管理", server: a.adminServer}, {name: "对外 MCP", server: a.publicMCPServer}} {
		if spec.server == nil {
			continue
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := spec.server.Shutdown(shutCtx); err != nil {
			a.logger.Warn("HTTP 服务优雅停机超时，强制关闭", "name", spec.name, "error", err)
			_ = spec.server.Close()
		}
		cancel()
	}

	// 2) 停止后台服务。
	if a.scheduler != nil {
		a.scheduler.Stop()
	}
	if a.statCleaner != nil {
		a.statCleaner.Stop()
	}
	if a.auditRecorder != nil {
		a.auditRecorder.Stop()
	}
	if a.statRecorder != nil {
		a.statRecorder.Stop()
	}
	if a.mgr != nil {
		a.mgr.Shutdown()
	}

	// 3) 释放基础设施连接。
	if err := a.closeInfra(); err != nil {
		a.logger.Warn("释放基础设施连接时出错", "error", err)
	}
	a.logger.Info("已完成优雅停机")
}
