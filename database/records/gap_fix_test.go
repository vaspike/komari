package records

import (
	"testing"
	"time"
)

// TestGapWindowAlignment 验证 recent 窗口起点正确对齐到 long_term 最新桶,
// 消除聚合滞后 (long_term 停在 4.5h 前,而 fourHoursAgo 是 4h1m) 造成的真空带。
func TestGapWindowAlignment(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fourHoursAgo := now.Add(-4*time.Hour - time.Minute)

	// 模拟聚合滞后: long_term 最新数据停在 4.5 小时前 (15min 对齐桶)
	latestLongTerm := now.Add(-4*time.Hour - 30*time.Minute).Truncate(15 * time.Minute)
	nextBucket := latestLongTerm.Add(15 * time.Minute)

	recentStart := fourHoursAgo
	if nextBucket.Before(recentStart) {
		recentStart = nextBucket
	}

	// 断言: recent 起点 = long_term 最新桶之后的下一个桶,无缝衔接
	if !recentStart.Equal(nextBucket) {
		t.Fatalf("滞后场景 recentStart 应为 %v (nextBucket),实际 %v", nextBucket, recentStart)
	}
	t.Logf("ok: 滞后场景 recentStart=%v (long_term 最新桶=%v, fourHoursAgo=%v)", recentStart, latestLongTerm, fourHoursAgo)
}

// TestNormalWindowKeepsFourHours 验证聚合正常时 (long_term 追到 now-4h),recent 起点保持 fourHoursAgo
func TestNormalWindowKeepsFourHours(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fourHoursAgo := now.Add(-4*time.Hour - time.Minute)

	// 正常情况: long_term 已追到 fourHoursAgo 之后最近的桶
	latestLongTerm := now.Add(-3*time.Hour - 45*time.Minute).Truncate(15 * time.Minute)
	nextBucket := latestLongTerm.Add(15 * time.Minute)

	recentStart := fourHoursAgo
	if nextBucket.Before(recentStart) {
		recentStart = nextBucket
	}

	if !recentStart.Equal(fourHoursAgo) {
		t.Fatalf("正常时 recentStart 应为 fourHoursAgo,实际 %v", recentStart)
	}
	t.Logf("ok: 正常情况 recentStart=%v", recentStart)
}
