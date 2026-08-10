//go:build ignore

// 该文件为 internal/handler/backup.go 的误置副本（错误地放在 internal/repository/
// 目录下但声明 package handler，且 bsonJSONValue 用 t.Code 无法在
// mongo-driver v1.17 编译）。用 //go:build ignore 排除出默认构建，避免与
// repository 包冲突，待后续确认后删除。正确版本见 internal/handler/backup.go。
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 备份允许导出的集合（对齐 routes/backup.js ALLOWED_EXPORT_COLLECTIONS）。
// 与 ALLOWED_IMPORT_COLLECTIONS 完全一致。
var allowedBackupCollections = []string{
	"episodes", "users", "categories", "banners", "ratings",
	"follows", "favorites", "histories", "notifications", "reports",
	"sitecontents", "singleepisodes", "creatorprofiles", "announcements",
}

// backupCollectionFields 导入时允许保留的字段白名单
// （对齐 routes/backup.js COLLECTION_FIELDS）。
var backupCollectionFields = map[string]map[string]bool{
	"episodes":        backupStrSet("title", "titleEn", "titleJa", "description", "descriptionEn", "descriptionJa", "coverImage", "totalEpisodes", "currentEpisodes", "status", "category", "tags", "updateDay", "premiereDate", "platformLinks", "views", "averageRating", "ratingCount", "reviewStatus", "reviewNote", "createdBy", "allowedEditors", "createdAt", "updatedAt"),
	"users":           backupStrSet("accountId", "username", "email", "isEmailVerified", "role", "avatar", "deletionRequestedAt", "createdAt", "updatedAt"),
	"categories":      backupStrSet("name", "nameEn", "nameJa", "description", "descriptionEn", "descriptionJa", "icon", "order", "createdAt"),
	"banners":         backupStrSet("title", "titleEn", "titleJa", "subtitle", "subtitleEn", "subtitleJa", "image", "link", "order", "active", "createdAt"),
	"ratings":         backupStrSet("userId", "episodeId", "score", "createdAt", "updatedAt"),
	"follows":         backupStrSet("userId", "episodeId", "folderId", "followedAtEpisodes", "createdAt"),
	"favorites":       backupStrSet("userId", "episodeId", "folderId", "createdAt", "updatedAt"),
	"histories":       backupStrSet("userId", "episodeId", "watchedEpisodes", "lastWatched", "createdAt", "updatedAt"),
	"notifications":   backupStrSet("userId", "episodeId", "episodeTitle", "episodeTitleEn", "type", "message", "isRead", "metadata", "createdAt"),
	"reports":         backupStrSet("reporterId", "targetType", "targetId", "reason", "description", "status", "resolveNote", "resolvedBy", "createdAt", "updatedAt"),
	"sitecontents":    backupStrSet("key", "title", "content", "createdAt", "updatedAt"),
	"singleepisodes":  backupStrSet("episodeId", "episodeNumber", "title", "titleEn", "titleJa", "duration", "platformLinks", "views", "scheduledDate", "isScheduled", "releaseDate", "premiereDate", "isUpcoming", "createdAt", "updatedAt"),
	"creatorprofiles": backupStrSet("creatorId", "displayName", "displayNameEn", "bio", "bioEn", "avatar", "socialLinks", "createdAt", "updatedAt"),
	"announcements":   backupStrSet("title", "titleEn", "content", "contentEn", "type", "showPopup", "showBanner", "sendNotification", "sendEmail", "dismissible", "active", "pinned", "publishAt", "expireAt", "notificationSent", "emailSent", "emailSentAt", "emailSentCount", "link", "createdBy", "createdAt", "updatedAt"),
}

// backupMaxImportBytes 单次导入备份数据大小上限（50MB，对齐 backup.js:69）。
const backupMaxImportBytes = 50 * 1024 * 1024

// backupMaxImportDocs 单集合导入数量上限（对齐 backup.js:97）。
const backupMaxImportDocs = 10000

// Backup 是数据备份域（/api/backup）handler 容器，行为逐端点对齐 backend/routes/backup.js。
type Backup struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
	// DB 提供原始集合访问（对齐 Express mongoose.connection.db）。
	DB *mongo.Database
}

// NewBackup 构造备份 handler 容器。db 用于 raw collection 读取/写入
// （对齐 backup.js 的 mongoose.connection.db.collection(col)）。
func NewBackup(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc, db *mongo.Database) *Backup {
	return &Backup{Repos: repos, Config: cfg, AuthMW: amw, RL: rl, DB: db}
}

// Register 挂载备份路由（路径照抄 Express 子路径，不含 /api 前缀）。
// 鉴权对齐 backup.js：adminProtect（creator/admin/superadmin）→ requireSuperAdmin。
func (h *Backup) Register(g *gin.RouterGroup) {
	protect := h.AuthMW.Protect(middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin)
	g.GET("/export", protect, h.requireSuperAdmin, h.Export)
	g.POST("/import", protect, h.requireSuperAdmin, h.Import)
}

// requireSuperAdmin 对齐 backup.js requireSuperAdmin：仅 superadmin 放行，
// 否则 403 {"message":"需要超级管理员权限"}。
func (h *Backup) requireSuperAdmin(c *gin.Context) {
	user, ok := middleware.GetUser(c)
	if !ok || user.Role != middleware.RoleSuperAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "需要超级管理员权限"})
		return
	}
	c.Next()
}

// Export GET /api/backup/export（adminProtect + superadmin）。
// @Summary 导出全库备份（JSON 下载）
// @Tags 备份
// @Security bearerAuth
// @Success 200 {object} map[string]any "各集合文档数组；users 剔除敏感字段"
// @Router /backup/export [get]
func (h *Backup) Export(c *gin.Context) {
	backup := gin.H{}
	ctx := c.Request.Context()
	for _, col := range allowedBackupCollections {
		docs, err := h.findAllDocs(ctx, col)
		if err != nil {
			continue // 对齐 try/catch {}：单集合失败跳过
		}
		if col == "users" {
			// 剔除 password / lastLoginIp / lastLoginRegion / deviceInfo（对齐 destructure）。
			out := make([]any, 0, len(docs))
			for _, d := range docs {
				m := d.(bson.M)
				delete(m, "password")
				delete(m, "lastLoginIp")
				delete(m, "lastLoginRegion")
				delete(m, "deviceInfo")
				out = append(out, bsonJSONValue(m))
			}
			backup[col] = out
		} else {
			out := make([]any, 0, len(docs))
			for _, d := range docs {
				out = append(out, bsonJSONValue(d))
			}
			backup[col] = out
		}
	}
	filename := "backup_" + time.Now().UTC().Format("2006-01-02") + ".json"
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.JSON(200, backup)
}

// Import POST /api/backup/import（adminProtect + superadmin）。
// @Summary 恢复备份数据
// @Tags 备份
// @Security bearerAuth
// @Accept json
// @Param body body object true "{data: 备份对象, overwrite: 是否覆盖}"
// @Success 200 {object} map[string]any "message/results"
// @Router /backup/import [post]
func (h *Backup) Import(c *gin.Context) {
	body := readBodyMap(c)
	dataVal, ok := body["data"]
	if !ok || dataVal == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "无效的备份数据"})
		return
	}
	var data map[string]any
	switch v := dataVal.(type) {
	case map[string]any:
		data = v
	case []any:
		// typeof [] === 'object' 且 truthy：Object.entries 按索引作为 key → 均非允许集合。
		data = make(map[string]any, len(v))
		for i, item := range v {
			data[strconv.Itoa(i)] = item
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"message": "无效的备份数据"})
		return
	}
	overwrite := truthy(body["overwrite"])

	// 大小校验（对齐 JSON.stringify(data).length > 50MB，UTF-16 长度）。
	dataJSON, err := json.Marshal(data)
	if err != nil {
		serverError(c)
		return
	}
	if jsUTF16Len(string(dataJSON)) > backupMaxImportBytes {
		c.JSON(http.StatusBadRequest, gin.H{"message": "备份数据过大，最大支持50MB"})
		return
	}

	ctx := c.Request.Context()
	results := gin.H{}
	for col, rawDocs := range data {
		if !backupStrContains(allowedBackupCollections, col) {
			results[col] = "skipped: not allowed"
			continue
		}
		docs, ok := rawDocs.([]any)
		if !ok || len(docs) == 0 {
			continue
		}
		cleanDocs := cleanBackupDocs(col, docs)
		if len(cleanDocs) == 0 {
			continue
		}
		if len(cleanDocs) > backupMaxImportDocs {
			results[col] = "skipped: too many documents (max 10000)"
			continue
		}
		coll := h.DB.Collection(col)
		if overwrite {
			session, err := h.DB.Client().StartSession()
			if err != nil {
				results[col] = "error: 导入失败"
				continue
			}
			_, terr := session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
				if _, err := coll.DeleteMany(sc, bson.M{}); err != nil {
					return nil, err
				}
				if _, err := coll.InsertMany(sc, cleanDocs, options.InsertMany().SetOrdered(false)); err != nil {
					return nil, err
				}
				return nil, nil
			})
			session.EndSession(context.Background())
			if terr != nil {
				results[col] = "error: 导入失败"
			} else {
				results[col] = len(cleanDocs)
			}
		} else {
			if _, err := coll.InsertMany(ctx, cleanDocs, options.InsertMany().SetOrdered(false)); err != nil {
				results[col] = "error: 导入失败"
			} else {
				results[col] = len(cleanDocs)
			}
		}
	}

	// 审计（对齐 logManual(... '数据恢复' '全库' ...)，失败静默）。
	h.logImportAudit(c, results)

	c.JSON(200, gin.H{"message": "数据恢复完成", "results": results})
}

// findAllDocs 读取集合全部文档（对齐 db.collection(col).find({}).toArray()）。
// 集合不存在返回空列表、nil 错误（MongoDB Find 空集合不报错）。
func (h *Backup) findAllDocs(ctx context.Context, col string) ([]bson.M, error) {
	cur, err := h.DB.Collection(col).Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := make([]bson.M, 0)
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// cleanBackupDocs 对齐 backup.js cleanDocs：剔除 _id/password，并按集合白名单过滤字段。
func cleanBackupDocs(col string, docs []any) []any {
	allowed, hasAllowed := backupCollectionFields[col]
	cleaned := make([]any, 0, len(docs))
	for _, d := range docs {
		m, ok := d.(map[string]any)
		if !ok {
			m = map[string]any{}
		}
		delete(m, "_id")
		delete(m, "password")
		if hasAllowed {
			filtered := map[string]any{}
			for k, v := range m {
				if allowed[k] {
					filtered[k] = v
				}
			}
			m = filtered
		}
		cleaned = append(cleaned, m)
	}
	return cleaned
}

// logImportAudit 写数据恢复审计日志（对齐 logManual 的 catch(()=>{}) 失败静默）。
func (h *Backup) logImportAudit(c *gin.Context, results gin.H) {
	user, ok := middleware.GetUser(c)
	if !ok {
		return
	}
	details, err := json.Marshal(results)
	if err != nil {
		return
	}
	name := user.Username
	if name == "" {
		name = user.AccountID
	}
	userID := user.ID
	_ = h.Repos.AuditLogs.Create(c.Request.Context(), &model.AuditLog{
		UserID:    &userID,
		UserName:  name,
		Action:    "数据恢复",
		Target:    "全库",
		Details:   string(details),
		CreatedAt: time.Now(),
	})
}

// ---- helpers ----

// strSet 构造字符串集合。
func backupStrSet(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

// readBodyMap 读取 JSON 请求体为 map；空体/非对象体返回空 map
// （对齐 episodes.go readJSONBody 的语义，供 translate/backup 域共用）。
func readBodyMap(c *gin.Context) map[string]any {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		return map[string]any{}
	}
	return body
}

// strSetContains 判断字符串是否在切片中（对齐 Array.includes）。
func backupStrContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// jsUTF16Len 计算 JS String.length 等价的 UTF-16 码元长度。
func jsUTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// bsonJSONValue 把 raw BSON 文档值转换为 JSON 友好值，逐类型对齐 mongoose toJSON：
//   - ObjectID → hex 字符串
//   - Date(DateTime/time.Time) → ISO 8601（毫秒 + Z，对齐 toISOString）
//   - 嵌套文档/数组 → 递归
//   - 其余基本类型原样
func bsonJSONValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case primitive.ObjectID:
		return t.Hex()
	case primitive.DateTime:
		return t.Time().UTC().Format("2006-01-02T15:04:05.000Z")
	case time.Time:
		return t.UTC().Format("2006-01-02T15:04:05.000Z")
	case bson.M:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = bsonJSONValue(val)
		}
		return out
	case primitive.M:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = bsonJSONValue(val)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = bsonJSONValue(val)
		}
		return out
	case bson.D:
		out := make(map[string]any, len(t))
		for _, e := range t {
			out[e.Key] = bsonJSONValue(e.Value)
		}
		return out
	case primitive.D:
		out := make(map[string]any, len(t))
		for _, e := range t {
			out[e.Key] = bsonJSONValue(e.Value)
		}
		return out
	case bson.A:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = bsonJSONValue(val)
		}
		return out
	case primitive.A:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = bsonJSONValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = bsonJSONValue(val)
		}
		return out
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	case int:
		return t
	case int32:
		return t
	case int64:
		return t
	case float32:
		return float64(t)
	case float64:
		return t
	case bool:
		return t
	case string:
		return t
	case primitive.Null:
		return nil
	case primitive.Decimal128:
		return t.String()
	case primitive.Binary:
		return base64.StdEncoding.EncodeToString(t.Data)
	case primitive.Timestamp:
		return t
	case primitive.Regex:
		return t.Pattern
	case primitive.JavaScript:
		return t.Code
	default:
		return fmt.Sprintf("%v", t)
	}
}
