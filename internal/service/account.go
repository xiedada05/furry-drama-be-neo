package service

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// DeletionResult 是注销申请状态。
type DeletionResult struct {
	RequestedAt time.Time
	DeleteAt    time.Time
}

// RequestDeletion POST /api/auth/request-deletion（protect）。
// 对齐 account.js L45-77：置 deletionRequestedAt + audit。
func (s *AuthService) RequestDeletion(ctx context.Context, user *model.User) (*DeletionResult, error) {
	if user.DeletionRequestedAt != nil {
		return nil, errors.New(400, "已提交过注销申请")
	}
	now := time.Now()
	user.DeletionRequestedAt = &now
	_ = s.Repos.Users.Save(ctx, user)
	s.Audit(ctx, user, "ACCOUNT_DELETION_REQUESTED", "auth", "User requested account deletion", "")
	return &DeletionResult{RequestedAt: now, DeleteAt: now.Add(DeletionGraceDays * 24 * time.Hour)}, nil
}

// CancelDeletion POST /api/auth/cancel-deletion（protect）。
// 对齐 account.js L79-106：清除申请 + audit。
func (s *AuthService) CancelDeletion(ctx context.Context, user *model.User) error {
	if user.DeletionRequestedAt == nil {
		return errors.New(400, "没有注销申请")
	}
	user.DeletionRequestedAt = nil
	_ = s.Repos.Users.Save(ctx, user)
	s.Audit(ctx, user, "ACCOUNT_DELETION_CANCELLED", "auth", "User cancelled account deletion", "")
	return nil
}

// DeletionStatus GET /api/auth/deletion-status（protect）。
// 对齐 account.js L108-120。
func (s *AuthService) DeletionStatus(user *model.User) gin.H {
	if user.DeletionRequestedAt == nil {
		return gin.H{"requested": false}
	}
	return gin.H{
		"requested":           true,
		"deletionRequestedAt": user.DeletionRequestedAt,
		"deleteAt":            user.DeletionRequestedAt.Add(DeletionGraceDays * 24 * time.Hour),
	}
}
