package auth

import "golang.org/x/crypto/bcrypt"

// bcrypt 口令哈希，行为对齐 Express models/User.js：
//   - bcryptjs 以 cost=12 生成盐（models/User.js:111 `bcrypt.genSalt(12)`）
//   - 校验用 bcrypt.compare（models/User.js:117）
//
// Go 的 x/crypto/bcrypt 与 bcryptjs 互操作：
//   - bcryptjs 默认生成 $2a$/$2b$ 前缀哈希，Go 均可校验（x/crypto/bcrypt
//     兼容 $2a$/$2b$/$2y$ 前缀）
//   - Go 生成的 $2a$ 哈希 bcryptjs 亦能校验
// 两者均实现 bcrypt 规范，可双向互验。

// BcryptCost 是口令哈希轮数，对齐 Express bcryptjs 的 genSalt(12)。
const BcryptCost = 12

// Hash 用 bcrypt（cost=12）计算口令哈希，返回标准 $2a$ 前缀的 bcrypt 字符串。
// 失败（如口令超长）返回错误。
func Hash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// Compare 常量时间校验口令与 bcrypt 哈希是否匹配，匹配返回 true。
// 哈希非法（格式错误/长度不符）返回 false。
func Compare(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
