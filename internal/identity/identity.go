// Package identity 本机长期身份：Ed25519 密钥对，ID = SHA256(公钥) 前 16 hex。
// 密钥持久化于 dataDir/identity.json（0600）。
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Identity 本机身份
type Identity struct {
	ID   string
	Pub  ed25519.PublicKey
	priv ed25519.PrivateKey

	path string // identity.json 路径，用于昵称落盘

	mu   sync.RWMutex
	name string
}

// Name 当前显示昵称（线程安全）
func (i *Identity) Name() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.name
}

// SetName 运行时修改显示昵称（线程安全，下次心跳广播生效），并持久化到磁盘
func (i *Identity) SetName(n string) {
	i.mu.Lock()
	i.name = n
	path := i.path
	i.mu.Unlock()

	if path == "" {
		return
	}
	// 读回现有密钥文件再写入昵称，避免覆盖公私钥
	if b, err := os.ReadFile(path); err == nil {
		var kf keyFile
		if json.Unmarshal(b, &kf) == nil {
			kf.Name = n
			if out, err := json.MarshalIndent(kf, "", "  "); err == nil {
				_ = os.WriteFile(path, out, 0o600)
			}
		}
	}
}

type keyFile struct {
	Pub  string `json:"pub"`
	Priv string `json:"priv"`
	Name string `json:"name"`
}

// IDFromPub 由公钥推导节点 ID
func IDFromPub(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8]) // 16 hex 字符
}

// LoadOrCreate 加载或首次生成身份
func LoadOrCreate(dataDir, name string) (*Identity, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("identity: 创建数据目录失败: %w", err)
	}
	path := filepath.Join(dataDir, "identity.json")

	if b, err := os.ReadFile(path); err == nil {
		var kf keyFile
		if err := json.Unmarshal(b, &kf); err != nil {
			return nil, fmt.Errorf("identity: 解析密钥文件失败: %w", err)
		}
		pub, err1 := hex.DecodeString(kf.Pub)
		priv, err2 := hex.DecodeString(kf.Priv)
		if err1 != nil || err2 != nil ||
			len(pub) != ed25519.PublicKeySize || len(priv) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("identity: 密钥文件损坏，请删除 %s 后重新生成", path)
		}
		// 优先使用已保存的昵称，仅当它为空时回退到默认（主机名）
		savedName := kf.Name
		if savedName == "" {
			savedName = name
		}
		return &Identity{ID: IDFromPub(pub), name: savedName, Pub: pub, priv: priv, path: path}, nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: 生成密钥失败: %w", err)
	}
	kf := keyFile{Pub: hex.EncodeToString(pub), Priv: hex.EncodeToString(priv), Name: name}
	b, _ := json.MarshalIndent(kf, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, fmt.Errorf("identity: 保存密钥失败: %w", err)
	}
	return &Identity{ID: IDFromPub(pub), name: name, Pub: pub, priv: priv, path: path}, nil
}

// Sign 用本机私钥签名
func (i *Identity) Sign(data []byte) []byte {
	return ed25519.Sign(i.priv, data)
}

// Verify 验证签名
func Verify(pub ed25519.PublicKey, data, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, data, sig)
}

// Fingerprint 公钥指纹（供 TOFU 展示）
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:16])
}
