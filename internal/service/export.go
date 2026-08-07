package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// ExportData 是 /export-my-data 的 JSON 结构（对齐 users.js L187-202）。
type ExportData struct {
	ExportDate   string   `json:"exportDate"`
	User         gin.H    `json:"user"`
	Follows      []gin.H  `json:"follows"`
	Favorites    []gin.H  `json:"favorites"`
	Ratings      []gin.H  `json:"ratings"`
	WatchHistory []gin.H  `json:"watchHistory"`
}

// BuildExportData 组装导出数据，populate episodeId 的 title/coverImage/status。
func (s *AuthService) BuildExportData(ctx context.Context, user *model.User) (*ExportData, error) {
	follows, err := s.Repos.Follows.FindByUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	favorites, err := s.Repos.Favorites.FindByUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	ratings, err := s.Repos.Ratings.FindByUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	history, err := s.Repos.Histories.FindByUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}

	followRows := make([]gin.H, 0, len(follows))
	for i := range follows {
		f := &follows[i]
		followRows = append(followRows, gin.H{
			"_id":                 f.ID.Hex(),
			"userId":              f.UserID.Hex(),
			"episodeId":           s.episodePopulate(ctx, f.EpisodeID, true),
			"folderId":            objectIDOrNil(f.FolderID),
			"followedAtEpisodes":  f.FollowedAtEpisodes,
			"createdAt":           f.CreatedAt,
		})
	}

	favRows := make([]gin.H, 0, len(favorites))
	for i := range favorites {
		f := &favorites[i]
		favRows = append(favRows, gin.H{
			"_id":        f.ID.Hex(),
			"userId":     f.UserID.Hex(),
			"episodeId":  s.episodePopulate(ctx, f.EpisodeID, true),
			"folderId":   objectIDOrNil(f.FolderID),
			"createdAt":  f.CreatedAt,
			"updatedAt":  f.UpdatedAt,
		})
	}

	ratingRows := make([]gin.H, 0, len(ratings))
	for i := range ratings {
		r := &ratings[i]
		ratingRows = append(ratingRows, gin.H{
			"_id":       r.ID.Hex(),
			"userId":    r.UserID.Hex(),
			"episodeId": s.episodePopulate(ctx, r.EpisodeID, false),
			"score":     r.Score,
			"createdAt": r.CreatedAt,
			"updatedAt": r.UpdatedAt,
		})
	}

	historyRows := make([]gin.H, 0, len(history))
	for i := range history {
		h := &history[i]
		historyRows = append(historyRows, gin.H{
			"_id":                     h.ID.Hex(),
			"userId":                  h.UserID.Hex(),
			"episodeId":               s.episodePopulate(ctx, h.EpisodeID, false),
			"watchedEpisodes":         h.WatchedEpisodes,
			"lastWatchedEpisodeNumber": h.LastWatchedEpisodeNumber,
			"lastWatched":             h.LastWatched,
			"createdAt":               h.CreatedAt,
		})
	}

	createdAt := ""
	if user.CreatedAt.IsZero() {
		createdAt = ""
	} else {
		createdAt = user.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return &ExportData{
		ExportDate: nowISO(),
		User: gin.H{
			"_id":             user.ID.Hex(),
			"accountId":       user.AccountID,
			"username":        user.Username,
			"email":           user.Email,
			"isEmailVerified": user.IsEmailVerified,
			"avatar":          user.Avatar,
			"createdAt":       createdAt,
		},
		Follows:      followRows,
		Favorites:    favRows,
		Ratings:      ratingRows,
		WatchHistory: historyRows,
	}, nil
}

// episodePopulate 把 episodeId 展开为 episode 基础文档（对齐 populate）。
func (s *AuthService) episodePopulate(ctx context.Context, id interface{}, withStatus bool) gin.H {
	ep, err := s.Repos.Episodes.FindBasicByID(ctx, id)
	if err != nil {
		return gin.H{"_id": objectIDHex(id)}
	}
	row := gin.H{"_id": ep.ID.Hex(), "title": ep.Title, "coverImage": ep.CoverImage}
	if withStatus {
		row["status"] = ep.Status
	}
	return row
}

// ExportCSV 生成 CSV（含 BOM 前缀，对齐 users.js L131-185）。
func (d *ExportData) ExportCSV() string {
	var b strings.Builder
	esc := func(s string) string {
		if strings.ContainsAny(s, ",\"\n") {
			return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
		}
		return s
	}
	b.WriteString("用户信息\n字段,值\n")
	b.WriteString("账号ID," + esc(d.User["accountId"].(string)) + "\n")
	b.WriteString("昵称," + esc(d.User["username"].(string)) + "\n")
	b.WriteString("邮箱," + esc(d.User["email"].(string)) + "\n")
	b.WriteString("注册时间," + esc(fmtTime(d.User["createdAt"])) + "\n\n")

	b.WriteString("关注列表\n剧名,状态\n")
	for _, f := range d.Follows {
		title, status := episodeTitleStatus(f["episodeId"])
		b.WriteString(esc(title) + "," + esc(status) + "\n")
	}
	b.WriteString("\n")

	b.WriteString("收藏列表\n剧名,状态\n")
	for _, f := range d.Favorites {
		title, status := episodeTitleStatus(f["episodeId"])
		b.WriteString(esc(title) + "," + esc(status) + "\n")
	}
	b.WriteString("\n")

	b.WriteString("评分记录\n剧名,评分\n")
	for _, r := range d.Ratings {
		title, _ := episodeTitleStatus(r["episodeId"])
		score := ""
		if v, ok := r["score"].(int); ok {
			score = itoa(v)
		}
		b.WriteString(esc(title) + "," + score + "\n")
	}
	b.WriteString("\n")

	b.WriteString("观看历史\n剧名,最后观看时间\n")
	for _, h := range d.WatchHistory {
		title, _ := episodeTitleStatus(h["episodeId"])
		b.WriteString(esc(title) + "," + esc(fmtTime(h["lastWatched"])) + "\n")
	}
	return b.String()
}

func episodeTitleStatus(v any) (title, status string) {
	if m, ok := v.(gin.H); ok {
		if t, ok := m["title"].(string); ok {
			title = t
		}
		if s, ok := m["status"].(string); ok {
			status = s
		}
	}
	return
}

// ---- helpers ----

// nowISO 返回 ISO8601 UTC 毫秒时间（对齐 toISOString）。
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// fmtTime 把 time.Time 输出为 ISO8601 字符串。
func fmtTime(v any) string {
	if t, ok := v.(time.Time); ok && !t.IsZero() {
		return t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if t, ok := v.(*time.Time); ok && t != nil {
		return t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return ""
}

// objectIDOrNil 把 *ObjectID 转为 hex 或 nil。
func objectIDOrNil(id *primitive.ObjectID) any {
	if id == nil {
		return nil
	}
	return id.Hex()
}

// objectIDHex 把 any(ObjectID) 转为 hex。
func objectIDHex(id any) string {
	if oid, ok := id.(primitive.ObjectID); ok {
		return oid.Hex()
	}
	return ""
}

// itoa 整数转字符串。
func itoa(v int) string {
	return strconv.Itoa(v)
}
