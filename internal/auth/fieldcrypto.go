package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

// fieldcrypto 实现字段级对称加密，对齐 Express utils/crypto.js：
//   - AES-256-CBC，key = sha256(ENCRYPTION_KEY 或 JWT_SECRET)（二选一，非拼接）
//   - 输出格式 "enc:<iv hex>:<ct hex>"（iv 16 随机字节）
//   - 已有 "enc:" 前缀或空值原样返回（不重复加密）
//
// 用途：twoFactorSecret / twoFactorBackupCodes / SiteContent email.pass。
// 必须兼容解密旧数据——算法与 key 派生与原实现一致。

// FieldKey 派生 32 字节 AES key：sha256(encryptionKey 或 jwtSecret) 的 hex 表示。
// 与原实现一致：优先 ENCRYPTION_KEY，缺省回退 JWT_SECRET。
func FieldKey(encryptionKey, jwtSecret string) string {
	src := jwtSecret
	if encryptionKey != "" {
		src = encryptionKey
	}
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:])
}

// EncryptField 加密明文。空值或已有 enc: 前缀原样返回。
func EncryptField(text, keyHex string) string {
	if text == "" || strings.HasPrefix(text, "enc:") {
		return text
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return text
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return text
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return text
	}
	// CBC 需要 PKCS7 填充。
	padded := pkcs7Pad([]byte(text), aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return "enc:" + hex.EncodeToString(iv) + ":" + hex.EncodeToString(out)
}

// DecryptField 解密密文。非 enc: 前缀原样返回（明文透传）。
func DecryptField(text, keyHex string) string {
	if !strings.HasPrefix(text, "enc:") {
		return text
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return text
	}
	parts := strings.SplitN(text[4:], ":", 2)
	if len(parts) != 2 {
		return text
	}
	iv, err1 := hex.DecodeString(parts[0])
	ct, err2 := hex.DecodeString(parts[1])
	if err1 != nil || err2 != nil || len(iv) != aes.BlockSize {
		return text
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return text
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	unpadded, err := pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return text
	}
	return string(unpadded)
}

// EncryptArray 逐元素加密。
func EncryptArray(list []string, keyHex string) []string {
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = EncryptField(v, keyHex)
	}
	return out
}

// DecryptArray 逐元素解密。
func DecryptArray(list []string, keyHex string) []string {
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = DecryptField(v, keyHex)
	}
	return out
}

// pkcs7Pad 标准 PKCS#7 填充（对齐 Node crypto 默认）。
func pkcs7Pad(data []byte, blockSize int) []byte {
	padLen := blockSize - len(data)%blockSize
	pad := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(data, pad...)
}

// pkcs7Unpad 去除 PKCS#7 填充，非法返回错误。
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid padding")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize || padLen > len(data) {
		return nil, errors.New("invalid padding length")
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return nil, errors.New("invalid padding byte")
		}
	}
	return data[:len(data)-padLen], nil
}
