// Package pagination 提供与 Express utils/paginate.js 等价的分页参数解析。
//
// 规则（对齐 utils/paginate.js:14-17）：
//   - page 默认 1，最小 1
//   - limit 默认 20，钳制在 [1,100]（上限 100 硬编码，尽管 src/config.js 声称 MAX=200）
package pagination

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

const (
	defaultPage  = 1
	defaultLimit = 20
	maxLimit     = 100
)

// Query 是一次分页查询的参数。
type Query struct {
	Page  int
	Limit int
}

// Parse 从 gin.Context 查询参数解析 page/limit，非法值回退默认。
func Parse(c *gin.Context) Query {
	return ParseQuery(c.Query("page"), c.Query("limit"))
}

// ParseQuery 解析 page/limit 字符串。
func ParseQuery(pageStr, limitStr string) Query {
	page := defaultPage
	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	}
	limit := defaultLimit
	if l, err := strconv.Atoi(limitStr); err == nil && l >= 1 {
		if l > maxLimit {
			l = maxLimit
		}
		limit = l
	}
	return Query{Page: page, Limit: limit}
}

// Skip 返回 Mongo 查询的 skip 值（(page-1)*limit）。
func (q Query) Skip() int64 {
	return int64((q.Page - 1) * q.Limit)
}

// TotalPages 计算总页数（total==0 → 0）。
func (q Query) TotalPages(total int64) int {
	if total == 0 {
		return 0
	}
	pages := int((total + int64(q.Limit) - 1) / int64(q.Limit))
	return pages
}

// Result 是分页响应体，对齐 Express 的 {list, page, limit, total, totalPages}。
type Result struct {
	List       any   `json:"list"`
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"totalPages"`
}
