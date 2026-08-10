package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// FriendLink 友情链接文档（models/FriendLink.js → friendlinks 集合）。
type FriendLink struct {
	ID            primitive.ObjectID  `bson:"_id,omitempty" json:"_id"`
	Name          string              `bson:"name" json:"name"`
	NameEn        string              `bson:"nameEn" json:"nameEn"`
	NameJa        string              `bson:"nameJa" json:"nameJa"`
	URL           string              `bson:"url" json:"url"`
	Logo          string              `bson:"logo" json:"logo"`
	Description   string              `bson:"description" json:"description"`
	DescriptionEn string              `bson:"descriptionEn" json:"descriptionEn"`
	DescriptionJa string              `bson:"descriptionJa" json:"descriptionJa"`
	Order         int                 `bson:"order" json:"order"`
	IsActive      bool                `bson:"isActive" json:"isActive"`
	Status        string              `bson:"status" json:"status"`
	ApplicantID   *primitive.ObjectID `bson:"applicantId" json:"applicantId"`
	CreatedAt     time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt     time.Time           `bson:"updatedAt" json:"updatedAt"`
	// VersionKey 是 mongoose 的 __v。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}
