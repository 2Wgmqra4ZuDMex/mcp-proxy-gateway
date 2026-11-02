package app

import (
	"context"
	"sort"
	"time"

	"github.com/myGithub/mcp-proxy-gateway/internal/aggregation"
	"github.com/myGithub/mcp-proxy-gateway/internal/httpapi"
	"github.com/myGithub/mcp-proxy-gateway/internal/store"
)

const (
	accountEventWindow = 7 * 24 * time.Hour
	accountEventLimit  = 500
)

// accountEventView 组合持久化的账号异常记录与聚合层的实时冷却状态。
type accountEventView struct {
	repo *store.AccountEventRepo
	agg  *aggregation.Service
}

func (v accountEventView) Status(ctx context.Context) (httpapi.AccountEventStatus, error) {
	now := time.Now().UTC()
	events, err := v.repo.ListSince(ctx, now.Add(-accountEventWindow), accountEventLimit)
	if err != nil {
		return httpapi.AccountEventStatus{}, err
	}
	status := httpapi.AccountEventStatus{Cooldowns: []httpapi.AccountCooldown{}, Accounts: []httpapi.AccountEventSummary{}, Events: events}
	byID := map[string]*httpapi.AccountEventSummary{}
	for _, ev := range events { // 时间倒序：每个账号第一条即最近一次
		s, ok := byID[ev.UpstreamID]
		if !ok {
			s = &httpapi.AccountEventSummary{UpstreamID: ev.UpstreamID, UpstreamName: ev.UpstreamName, LastKind: ev.Kind,
				LastStatus: ev.HTTPStatus, LastMessage: ev.Message, LastTool: ev.Tool, LastAt: ev.OccurredAt}
			byID[ev.UpstreamID] = s
		}
		s.Last7d++
		if now.Sub(ev.OccurredAt) <= 24*time.Hour {
			s.Last24h++
		}
	}
	for _, s := range byID {
		status.Accounts = append(status.Accounts, *s)
	}
	sort.Slice(status.Accounts, func(i, j int) bool { return status.Accounts[i].LastAt.After(status.Accounts[j].LastAt) })
	if v.agg != nil {
		for _, l := range v.agg.AccountLimits() {
			status.Cooldowns = append(status.Cooldowns, httpapi.AccountCooldown{UpstreamID: l.UpstreamID, Kind: l.Kind, Message: l.Message, Until: l.Until})
		}
		sort.Slice(status.Cooldowns, func(i, j int) bool { return status.Cooldowns[i].Until.Before(status.Cooldowns[j].Until) })
	}
	return status, nil
}

func (v accountEventView) Clear(ctx context.Context, upstreamID string) error {
	if err := v.repo.Clear(ctx, upstreamID); err != nil {
		return err
	}
	if v.agg != nil {
		v.agg.ClearAccountLimit(upstreamID)
	}
	return nil
}
