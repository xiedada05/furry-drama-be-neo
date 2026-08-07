package repository

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ToObjectID 把 hex 字符串形式的 ID 规范化为 primitive.ObjectID；
// 非字符串或非法 hex 原样返回。用于 _id 查询参数（JWT claims 中的 id 是字符串）。
func ToObjectID(id any) any {
	if s, ok := id.(string); ok {
		if oid, err := primitive.ObjectIDFromHex(s); err == nil {
			return oid
		}
	}
	return id
}
