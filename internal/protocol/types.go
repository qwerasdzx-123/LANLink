package protocol

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// ── 消息类型 ────────────────────────────────────────────────
// UDP 信令通道（45450）
const (
	TOnline    uint16 = 0x0001 // 上线通告（广播/单播探测）
	TOnlineAck uint16 = 0x0002 // 上线应答（单播）
	THeartbeat uint16 = 0x0003 // 心跳（广播）
	TOffline   uint16 = 0x0004 // 主动下线（广播）
	TAck       uint16 = 0x0010 // 可靠 UDP 确认
	TMsgText   uint16 = 0x0101 // 文本消息（单聊/群聊/广播）
	THsInit    uint16 = 0x0201 // 加密握手：发起
	THsResp    uint16 = 0x0202 // 加密握手：应答
	TFileOffer uint16 = 0x0301 // 文件传输请求（含清单）
	TFileAns   uint16 = 0x0302 // 文件传输应答（接受/拒绝）
)

// TCP 数据通道（45451）
const (
	THello    uint16 = 0x1001 // 连接绑定（task_id + token）
	TPull     uint16 = 0x1002 // 拉取某文件（含续传偏移）
	TChunk    uint16 = 0x1003 // 数据块
	TFileEnd  uint16 = 0x1004 // 单文件传输完成（含哈希）
	TTaskDone uint16 = 0x1005 // 整个任务完成
	TRate     uint16 = 0x1006 // 速率反馈/调整
	TErr      uint16 = 0x100F // 错误
)

// ── 载荷结构（CBOR 序列化）────────────────────────────────────

// PeerInfo 上线/心跳载荷
type PeerInfo struct {
	ID      string `cbor:"1,keyasint"`
	Name    string `cbor:"2,keyasint"`
	UDPPort int    `cbor:"3,keyasint"`
	TCPPort int    `cbor:"4,keyasint"`
	PubKey  []byte `cbor:"5,keyasint"` // Ed25519 公钥
	TS      int64  `cbor:"6,keyasint"` // Unix 毫秒
	Sig     []byte `cbor:"7,keyasint"` // Ed25519 签名，防伪造上线
}

// TextMsg 文本消息载荷
type TextMsg struct {
	From     string `cbor:"1,keyasint"`
	FromName string `cbor:"2,keyasint"`
	Group    string `cbor:"3,keyasint,omitempty"` // 群聊名（空=单聊/广播）
	TS       int64  `cbor:"4,keyasint"`
	Enc      bool   `cbor:"5,keyasint"`           // true: Cipher 有效；false: Body 有效
	Body     string `cbor:"6,keyasint,omitempty"` // 明文（仅广播）
	Cipher   []byte `cbor:"7,keyasint,omitempty"` // AES-256-GCM 密文（单聊/群聊）
	Bcast    bool   `cbor:"8,keyasint,omitempty"` // 是否广播消息
	Sig      []byte `cbor:"9,keyasint,omitempty"` // 广播明文的签名
}

// HsMsg 握手载荷（X25519 临时公钥 + Ed25519 签名）
type HsMsg struct {
	From string `cbor:"1,keyasint"`
	Eph  []byte `cbor:"2,keyasint"` // X25519 临时公钥
	Sig  []byte `cbor:"3,keyasint"` // 对 eph||from||to 的签名，绑定身份防中间人
}

// FileEntry 清单条目
type FileEntry struct {
	Rel    string `cbor:"1,keyasint"` // 相对路径（斜杠分隔）
	Size   int64  `cbor:"2,keyasint"`
	SHA256 string `cbor:"3,keyasint,omitempty"` // 十六进制
	IsDir  bool   `cbor:"4,keyasint,omitempty"`
}

// FileOffer 文件传输请求
type FileOffer struct {
	TaskID   string      `cbor:"1,keyasint"`
	From     string      `cbor:"2,keyasint"`
	FromName string      `cbor:"3,keyasint"`
	Token    string      `cbor:"4,keyasint"` // TCP 搭线凭证
	TCPPort  int         `cbor:"5,keyasint"`
	Files    []FileEntry `cbor:"6,keyasint"`
	Total    int64       `cbor:"7,keyasint"`
}

// FileAnswer 文件传输应答
type FileAnswer struct {
	TaskID string `cbor:"1,keyasint"`
	From   string `cbor:"2,keyasint"`
	Accept bool   `cbor:"3,keyasint"`
	Reason string `cbor:"4,keyasint,omitempty"`
}

// Hello TCP 连接绑定
type Hello struct {
	From   string `cbor:"1,keyasint"`
	TaskID string `cbor:"2,keyasint"`
	Token  string `cbor:"3,keyasint"`
}

// Pull 拉取文件请求
type Pull struct {
	Index  int   `cbor:"1,keyasint"` // 清单下标
	Offset int64 `cbor:"2,keyasint"` // 续传偏移
}

// Chunk 数据块
type Chunk struct {
	Index  int    `cbor:"1,keyasint"`
	Offset int64  `cbor:"2,keyasint"`
	Data   []byte `cbor:"3,keyasint"`
}

// FileEnd 单文件结束
type FileEnd struct {
	Index  int    `cbor:"1,keyasint"`
	SHA256 string `cbor:"2,keyasint"`
}

// RateCtl 速率控制
type RateCtl struct {
	BytesPerSec int64 `cbor:"1,keyasint"` // 0 = 不限速
}

// ErrMsg 错误
type ErrMsg struct {
	Code int    `cbor:"1,keyasint"`
	Msg  string `cbor:"2,keyasint"`
}

// ── 编解码辅助 ──────────────────────────────────────────────

// NewFrame 构造帧：CBOR 编码载荷
func NewFrame(typ uint16, v any) (*Frame, error) {
	var payload []byte
	if v != nil {
		b, err := cbor.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("protocol: 编码 0x%04X 载荷失败: %w", typ, err)
		}
		payload = b
	}
	return &Frame{Type: typ, Payload: payload}, nil
}

// DecodePayload 解码帧载荷
func DecodePayload(f *Frame, v any) error {
	if err := cbor.Unmarshal(f.Payload, v); err != nil {
		return fmt.Errorf("protocol: 解码 0x%04X 载荷失败: %w", f.Type, err)
	}
	return nil
}
