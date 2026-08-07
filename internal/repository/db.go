package repository

import (
	"context"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Connect 建立 MongoDB 连接并 Ping 验证，返回指定库的 Database。
// pool 为连接池大小（对齐 config/db.js maxPoolSize）。
// name 为空时从 URI 的 path 提取库名。
func Connect(ctx context.Context, uri, name string, pool int) (*mongo.Database, error) {
	if pool <= 0 {
		pool = 10
	}
	clientOpts := options.Client().
		ApplyURI(uri).
		SetMaxPoolSize(uint64(pool)).
		SetServerSelectionTimeout(10 * time.Second).
		SetSocketTimeout(45 * time.Second)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		return nil, err
	}
	if name == "" {
		name = dbNameFromURI(uri)
	}
	return client.Database(name), nil
}

// dbNameFromURI 从连接串提取库名（末尾路径段，去掉查询参数）。
func dbNameFromURI(uri string) string {
	if idx := strings.IndexByte(uri, '?'); idx >= 0 {
		uri = uri[:idx]
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "furry_drama_tracker"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return "furry_drama_tracker"
	}
	return parts[len(parts)-1]
}
