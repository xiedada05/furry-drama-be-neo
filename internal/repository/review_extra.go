package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// 本文件为 review 域（backend/routes/review.js）的数据访问方法。
// 方法统一以 Review 前缀命名，挂到已有 repo 类型上，避免与其它域 agent 重名。

// ReviewUserRef 是审核列表 populate 输出的用户引用视图
// （_id + accountId + username，可选 email）。
type ReviewUserRef struct {
	ID        primitive.ObjectID `bson:"_id" json:"_id"`
	AccountID string             `bson:"accountId" json:"accountId"`
	Username  string             `bson:"username" json:"username"`
	Email     string             `bson:"email" json:"email"`
}

// ReviewFindUserRefsByIDs 批量查用户引用（accountId/username/email），返回 hex → 视图。
// 对齐 review.js populate('createdBy'/'customAuthors'/'allowedEditors',
// 'accountId username[ email]')：不存在的用户不出现在映射中（调用方渲染 null）。
func (r *UserRepo) ReviewFindUserRefsByIDs(ctx context.Context, ids []primitive.ObjectID) (map[string]ReviewUserRef, error) {
	out := make(map[string]ReviewUserRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"accountId": 1, "username": 1, "email": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var u ReviewUserRef
		if err := cur.Decode(&u); err != nil {
			return nil, err
		}
		out[u.ID.Hex()] = u
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReviewMailTarget 是审核结果邮件收件人信息（对齐 notifyHelper.js
// sendNotificationEmailToUser 的 User.findById(userId).select('email isEmailVerified
// emailNotificationPrefs')）。ReviewResultPref 为 nil 视为未关闭（允许发送）。
type ReviewMailTarget struct {
	ID               primitive.ObjectID `bson:"_id"`
	Email            string             `bson:"email"`
	IsEmailVerified  bool               `bson:"isEmailVerified"`
	ReviewResultPref *bool              `bson:"reviewResult"`
}

// ReviewFindMailTargetByID 查单用户邮件收件人信息（reviewResult 偏好）。
func (r *UserRepo) ReviewFindMailTargetByID(ctx context.Context, id primitive.ObjectID) (*ReviewMailTarget, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	target := &ReviewMailTarget{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(bson.M{
			"email": 1, "isEmailVerified": 1, "emailNotificationPrefs.reviewResult": 1,
		})).Decode(target)
	return target, err
}
