package concurrent

import "math"

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func calculateStats(results []TestResult) map[string]interface{} {
	total := len(results)
	success := 0
	var latencies []float64
	var minLat, maxLat, sumLat float64

	for _, r := range results {
		if r.Status == 200 {
			success++
			latencies = append(latencies, r.LatencyMs)
			sumLat += r.LatencyMs
			if minLat == 0 || r.LatencyMs < minLat {
				minLat = r.LatencyMs
			}
			if r.LatencyMs > maxLat {
				maxLat = r.LatencyMs
			}
		}
	}

	report := map[string]interface{}{
		"total":   total,
		"success": success,
		"failed":  total - success,
	}

	if len(latencies) > 0 {
		sort.Float64s(latencies)
		avg := sumLat / float64(len(latencies))
		report["lat_avg"] = avg
		report["lat_min"] = minLat
		report["lat_max"] = maxLat
		report["lat_p50"] = latencies[min(int(float64(len(latencies))*0.5), len(latencies)-1)]
		report["lat_p95"] = latencies[min(int(float64(len(latencies))*0.95), len(latencies)-1)]

		var sumSqDiff float64
		for _, l := range latencies {
			diff := l - avg
			sumSqDiff += diff * diff
		}
		report["stdev"] = math.Sqrt(sumSqDiff / float64(len(latencies)))
	}

	return report
}
