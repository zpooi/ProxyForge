package test

import "fmt"

// Score 综合评分，越低越好。
// latencyMs：平均延迟（毫秒）
// packetLoss：丢包率 0.0 ~ 1.0
// speedBps：下载速度（字节/秒）
func Score(latencyMs int, packetLoss float64, speedBps int) float64 {
	if speedBps < 1 {
		speedBps = 1
	}
	speedPart := float64(1_000_000) / float64(speedBps) * 1000.0
	return float64(latencyMs) + packetLoss*2000.0 + speedPart
}

func FormatScore(s float64) string {
	return fmt.Sprintf("%.2f", s)
}
