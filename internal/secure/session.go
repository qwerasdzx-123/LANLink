// Package secure 端到端加密会话：
//
//	握手  Ed25519 签名的 X25519 临时密钥交换（绑定身份，防中间人）
//	派生  HKDF-SHA256(共享密钥) → 两条 32B 方向密钥
//	加密  AES-256-GCM，格式 nonce(12B) || ciphertext
package secure

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"lanlink/internal/identity"
	"lanlink/internal/protocol"
	"lanlink/internal/transport"
)

// PeerDirectory 由节点发现模块实现：提供对端公钥（TOFU 钉扎）与地址
type PeerDirectory interface {
	PeerPubKey(peerID string) (ed25519.PublicKey, bool)
	PeerUDPAddr(peerID string) (*net.UDPAddr, bool)
}

var (
	ErrNoPeer      = errors.New("secure: 未知节点（尚未被发现）")
	ErrNoSession   = errors.New("secure: 会话未建立")
	ErrHsTimeout   = errors.New("secure: 握手超时")
	ErrBadHsSig    = errors.New("secure: 握手签名验证失败（可能存在中间人）")
	ErrOpenFailure = errors.New("secure: 解密失败（密文被篡改或会话密钥不一致）")
)

type session struct {
	send cipher.AEAD
	recv cipher.AEAD
}

type pendingHS struct {
	ephPriv []byte
	done    chan error
}

// Manager 会话管理器
type Manager struct {
	id  *identity.Identity
	udp *transport.UDP
	dir PeerDirectory

	mu       sync.Mutex
	sessions map[string]*session
	pending  map[string]*pendingHS
}

func NewManager(id *identity.Identity, udp *transport.UDP, dir PeerDirectory) *Manager {
	m := &Manager{
		id:       id,
		udp:      udp,
		dir:      dir,
		sessions: make(map[string]*session),
		pending:  make(map[string]*pendingHS),
	}
	udp.RegisterHandler(protocol.THsInit, m.handleInit)
	udp.RegisterHandler(protocol.THsResp, m.handleResp)
	return m
}

// Has 是否已有会话
func (m *Manager) Has(peerID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[peerID]
	return ok
}

// Drop 对端离线时清除会话（重新上线后重新握手）
func (m *Manager) Drop(peerID string) {
	m.mu.Lock()
	delete(m.sessions, peerID)
	m.mu.Unlock()
}

// EnsureSession 确保与对端建立加密会话；无会话则发起握手并等待完成
func (m *Manager) EnsureSession(ctx context.Context, peerID string) error {
	m.mu.Lock()
	if _, ok := m.sessions[peerID]; ok {
		m.mu.Unlock()
		return nil
	}
	if p, ok := m.pending[peerID]; ok { // 已有进行中的握手，共享等待
		m.mu.Unlock()
		return m.wait(ctx, p.done)
	}
	ephPriv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, ephPriv); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("secure: 生成临时密钥失败: %w", err)
	}
	p := &pendingHS{ephPriv: ephPriv, done: make(chan error, 1)}
	m.pending[peerID] = p
	m.mu.Unlock()

	err := m.sendHandshake(ctx, peerID, protocol.THsInit, ephPriv)
	if err != nil {
		m.finishPending(peerID, err)
		return err
	}
	return m.wait(ctx, p.done)
}

func (m *Manager) wait(ctx context.Context, done chan error) error {
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return ErrHsTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) sendHandshake(ctx context.Context, peerID string, typ uint16, ephPriv []byte) error {
	addr, ok := m.dir.PeerUDPAddr(peerID)
	if !ok {
		return ErrNoPeer
	}
	ephPub, err := curve25519.X25519(ephPriv, curve25519.Basepoint)
	if err != nil {
		return fmt.Errorf("secure: 计算临时公钥失败: %w", err)
	}
	msg := protocol.HsMsg{
		From: m.id.ID,
		Eph:  ephPub,
		Sig:  m.id.Sign(hsDigest(ephPub, m.id.ID, peerID)),
	}
	f, err := protocol.NewFrame(typ, &msg)
	if err != nil {
		return err
	}
	return m.udp.SendReliable(ctx, addr, f)
}

// handleInit 响应方：验签 → 生成己方临时密钥 → 立即可算出会话 → 回复 RESP
func (m *Manager) handleInit(f *protocol.Frame, from *net.UDPAddr) {
	var msg protocol.HsMsg
	if err := protocol.DecodePayload(f, &msg); err != nil {
		return
	}
	if err := m.verifyHs(&msg); err != nil {
		log.Printf("[secure] 拒绝来自 %s 的握手: %v", from, err)
		return
	}
	ephPriv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, ephPriv); err != nil {
		return
	}
	if err := m.establish(msg.From, ephPriv, msg.Eph); err != nil {
		log.Printf("[secure] 建立会话失败: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.sendHandshake(ctx, msg.From, protocol.THsResp, ephPriv); err != nil {
		log.Printf("[secure] 回复握手失败: %v", err)
	}
}

// handleResp 发起方：验签 → 用 pending 的临时私钥算出会话
func (m *Manager) handleResp(f *protocol.Frame, from *net.UDPAddr) {
	var msg protocol.HsMsg
	if err := protocol.DecodePayload(f, &msg); err != nil {
		return
	}
	if err := m.verifyHs(&msg); err != nil {
		log.Printf("[secure] 拒绝来自 %s 的握手应答: %v", from, err)
		return
	}
	m.mu.Lock()
	p, ok := m.pending[msg.From]
	m.mu.Unlock()
	if !ok {
		return // 无进行中的握手（可能是重复应答）
	}
	err := m.establish(msg.From, p.ephPriv, msg.Eph)
	m.finishPending(msg.From, err)
}

func (m *Manager) finishPending(peerID string, err error) {
	m.mu.Lock()
	p, ok := m.pending[peerID]
	if ok {
		delete(m.pending, peerID)
	}
	m.mu.Unlock()
	if ok {
		p.done <- err
		close(p.done)
	}
}

// verifyHs 用 TOFU 钉扎的公钥验证握手签名（身份绑定，防中间人）
func (m *Manager) verifyHs(msg *protocol.HsMsg) error {
	pub, ok := m.dir.PeerPubKey(msg.From)
	if !ok {
		return ErrNoPeer
	}
	if !identity.Verify(pub, hsDigest(msg.Eph, msg.From, m.id.ID), msg.Sig) {
		return ErrBadHsSig
	}
	return nil
}

// establish 计算共享密钥并派生方向密钥
func (m *Manager) establish(peerID string, myEphPriv, peerEphPub []byte) error {
	shared, err := curve25519.X25519(myEphPriv, peerEphPub)
	if err != nil {
		return fmt.Errorf("secure: ECDH 失败: %w", err)
	}
	lo, hi := m.id.ID, peerID
	if lo > hi {
		lo, hi = hi, lo
	}
	kdf := hkdf.New(sha256.New, shared, []byte("llp-v1-hs"), []byte(lo+hi))
	keys := make([]byte, 64)
	if _, err := io.ReadFull(kdf, keys); err != nil {
		return fmt.Errorf("secure: HKDF 派生失败: %w", err)
	}
	// 约定：ID 较小方使用前 32B 作为发送密钥
	var sendKey, recvKey []byte
	if m.id.ID == lo {
		sendKey, recvKey = keys[:32], keys[32:]
	} else {
		sendKey, recvKey = keys[32:], keys[:32]
	}
	sendAEAD, err := newAEAD(sendKey)
	if err != nil {
		return err
	}
	recvAEAD, err := newAEAD(recvKey)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.sessions[peerID] = &session{send: sendAEAD, recv: recvAEAD}
	m.mu.Unlock()
	return nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secure: AES 初始化失败: %w", err)
	}
	return cipher.NewGCM(block)
}

// Seal 加密：返回 nonce||ciphertext
func (m *Manager) Seal(peerID string, plain []byte) ([]byte, error) {
	m.mu.Lock()
	s, ok := m.sessions[peerID]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNoSession
	}
	nonce := make([]byte, s.send.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secure: 生成 nonce 失败: %w", err)
	}
	return s.send.Seal(nonce, nonce, plain, nil), nil
}

// Open 解密 nonce||ciphertext
func (m *Manager) Open(peerID string, sealed []byte) ([]byte, error) {
	m.mu.Lock()
	s, ok := m.sessions[peerID]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNoSession
	}
	ns := s.recv.NonceSize()
	if len(sealed) < ns {
		return nil, ErrOpenFailure
	}
	plain, err := s.recv.Open(nil, sealed[:ns], sealed[ns:], nil)
	if err != nil {
		return nil, ErrOpenFailure
	}
	return plain, nil
}

// hsDigest 握手签名摘要：H("llp-hs" || eph || from || to)
func hsDigest(eph []byte, from, to string) []byte {
	h := sha256.New()
	h.Write([]byte("llp-hs"))
	h.Write(eph)
	h.Write([]byte(from))
	h.Write([]byte(to))
	return h.Sum(nil)
}
