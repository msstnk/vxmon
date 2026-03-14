package ui

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// table.go formats header/data rows into fixed-width strings for pane rendering.
// FormatTable is called from app/bottom_view for FDB, Neigh, and Route tables.
func FormatTable(headers []string, rows [][]string, maxWidth int) (string, []string) {
	colWidths := fitColumnWidths(headers, rows, maxWidth)

	var headStr string
	if len(headers) > 0 {
		headStr = formatRow(headers, colWidths)
	}

	res := make([]string, 0, len(rows))
	for _, row := range rows {
		res = append(res, formatRow(row, colWidths))
	}
	return headStr, res
}

// FormatRows is called from app/top_view.go to align rows without a header line.
func FormatRows(rows [][]string, maxWidth int) []string {
	colWidths := fitColumnWidths(nil, rows, maxWidth)
	res := make([]string, 0, len(rows))
	for _, row := range rows {
		res = append(res, formatRow(row, colWidths))
	}
	return res
}

func fitColumnWidths(headers []string, rows [][]string, maxWidth int) []int {
	colCount := len(headers)
	for _, row := range rows {
		if len(row) > colCount {
			colCount = len(row)
		}
	}
	if colCount == 0 {
		return nil
	}

	colWidths := make([]int, colCount)
	nonEmptyCounts := make([]int, colCount)
	nonEmptySums := make([]int, colCount)
	nonEmptyMax := make([]int, colCount)

	for i, h := range headers {
		if w := lipgloss.Width(h); w > colWidths[i] {
			colWidths[i] = w
		}
	}
	for _, row := range rows {
		for i, col := range row {
			w := lipgloss.Width(col)
			if w > colWidths[i] {
				colWidths[i] = w
			}
			if col == "" {
				continue
			}
			nonEmptyCounts[i]++
			nonEmptySums[i] += w
			if w > nonEmptyMax[i] {
				nonEmptyMax[i] = w
			}
		}
	}

	if maxWidth <= 0 {
		return colWidths
	}

	separatorWidth := 0
	if colCount > 1 {
		separatorWidth = 2 * (colCount - 1)
	}
	totalWidth := separatorWidth
	for _, w := range colWidths {
		totalWidth += w
	}
	if totalWidth <= maxWidth {
		return colWidths
	}

	type colStat struct {
		idx int
		avg int
		diff int
		max int
	}

	stats := make([]colStat, 0, colCount)
	for i := range colWidths {
		avg := colWidths[i]
		if nonEmptyCounts[i] > 0 {
			avg = int(math.Ceil(float64(nonEmptySums[i]) / float64(nonEmptyCounts[i])))
		}
		maxVal := colWidths[i]
		if nonEmptyMax[i] > maxVal {
			maxVal = nonEmptyMax[i]
		}
		diff := maxVal - avg
		if diff < 0 {
			diff = 0
		}
		stats = append(stats, colStat{
			idx:  i,
			avg:  avg,
			diff: diff,
			max:  maxVal,
		})
	}

	sort.Slice(stats, func(i, j int) bool {
		if stats[i].diff != stats[j].diff {
			return stats[i].diff > stats[j].diff
		}
		if stats[i].max != stats[j].max {
			return stats[i].max > stats[j].max
		}
		return stats[i].idx < stats[j].idx
	})

	overflow := totalWidth - maxWidth
	for _, stat := range stats {
		if overflow <= 0 {
			break
		}
		if stat.diff <= 0 {
			continue
		}
		target := stat.avg
		if target < 1 {
			target = 1
		}
		reducible := colWidths[stat.idx] - target
		if reducible <= 0 {
			continue
		}
		if reducible > overflow {
			reducible = overflow
		}
		colWidths[stat.idx] -= reducible
		overflow -= reducible
	}

	sort.Slice(stats, func(i, j int) bool {
		if stats[i].avg != stats[j].avg {
			return stats[i].avg > stats[j].avg
		}
		if colWidths[stats[i].idx] != colWidths[stats[j].idx] {
			return colWidths[stats[i].idx] > colWidths[stats[j].idx]
		}
		return stats[i].idx < stats[j].idx
	})

	for _, stat := range stats {
		if overflow <= 0 {
			break
		}
		reducible := colWidths[stat.idx] - 1
		if reducible <= 0 {
			continue
		}
		if reducible > overflow {
			reducible = overflow
		}
		colWidths[stat.idx] -= reducible
		overflow -= reducible
	}

	return colWidths
}

func formatRow(row []string, colWidths []int) string {
	parts := make([]string, 0, min(len(row), len(colWidths)))
	for i, col := range row {
		if i >= len(colWidths) {
			break
		}
		truncated := truncateToWidth(col, colWidths[i])
		parts = append(parts, fmt.Sprintf("%-*s", colWidths[i], truncated))
	}
	return strings.Join(parts, "  ")
}

func truncateToWidth(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= limit {
		return s
	}
	r := []rune(s)
	if limit <= 2 {
		if len(r) > limit {
			return string(r[:limit])
		}
		return s
	}
	if len(r) > limit-2 {
		return string(r[:limit-2]) + ".."
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
