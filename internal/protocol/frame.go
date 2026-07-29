// Package protocol 实现 LLP v1 帧格式：16 字节二进制帧头 + CBOR 载荷。
//
// 帧头布局（大端序）:
//
//	0  2B  MAGIC   0x4C50 "LP"
//	2  1B  VER     协议版本 = 1
//	3  1B  FLAGS   bit0=需要ACK  bit1=载荷已加密
//	4  2B  TYPE    消息类型
//	6  2B  保留
//	8  4B  MID     消息ID（可靠传输/去重用）
//	12 4B  LEN     载荷长度
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	Magic      uint16 = 0x4C50
	Version    byte   = 1
	HeaderSize        = 16

	// MaxUDPPayload UDP 单帧载荷上限（超过应改走 TCP 或报错）
	MaxUDPPayload = 60_000
	// MaxTCPPayload TCP 单帧载荷上限
	MaxTCPPayload = 1 << 20
)

// 帧标志位
const (
	FlagNeedAck byte = 1 << 0 // 需要对端回 ACK
	FlagEnc     byte = 1 << 1 // 载荷经 AES-256-GCM 加密
)

var (
	ErrBadMagic  = errors.New("protocol: 非法帧魔数")
	ErrBadVer    = errors.New("protocol: 不支持的协议版本")
	ErrTooLarge  = errors.New("protocol: 载荷超过上限")
	ErrTruncated = errors.New("protocol: 帧不完整")
)

// Frame LLP 协议帧
type Frame struct {
	Flags   byte
	Type    uint16
	MID     uint32
	Payload []byte
}

// Encode 序列化为线上字节
func (f *Frame) Encode() []byte {
	buf := make([]byte, HeaderSize+len(f.Payload))
	binary.BigEndian.PutUint16(buf[0:2], Magic)
	buf[2] = Version
	buf[3] = f.Flags
	binary.BigEndian.PutUint16(buf[4:6], f.Type)
	binary.BigEndian.PutUint32(buf[8:12], f.MID)
	binary.BigEndian.PutUint32(buf[12:16], uint32(len(f.Payload)))
	copy(buf[HeaderSize:], f.Payload)
	return buf
}

// Decode 从完整数据报解析（UDP 场景）
func Decode(b []byte) (*Frame, error) {
	if len(b) < HeaderSize {
		return nil, ErrTruncated
	}
	if binary.BigEndian.Uint16(b[0:2]) != Magic {
		return nil, ErrBadMagic
	}
	if b[2] != Version {
		return nil, ErrBadVer
	}
	plen := binary.BigEndian.Uint32(b[12:16])
	if plen > MaxTCPPayload {
		return nil, ErrTooLarge
	}
	if len(b) < HeaderSize+int(plen) {
		return nil, ErrTruncated
	}
	f := &Frame{
		Flags: b[3],
		Type:  binary.BigEndian.Uint16(b[4:6]),
		MID:   binary.BigEndian.Uint32(b[8:12]),
	}
	f.Payload = make([]byte, plen)
	copy(f.Payload, b[HeaderSize:HeaderSize+plen])
	return f, nil
}

// ReadFrame 从流中读取一帧（TCP 场景）
func ReadFrame(r io.Reader) (*Frame, error) {
	hdr := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint16(hdr[0:2]) != Magic {
		return nil, ErrBadMagic
	}
	if hdr[2] != Version {
		return nil, ErrBadVer
	}
	plen := binary.BigEndian.Uint32(hdr[12:16])
	if plen > MaxTCPPayload {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, plen)
	}
	f := &Frame{
		Flags: hdr[3],
		Type:  binary.BigEndian.Uint16(hdr[4:6]),
		MID:   binary.BigEndian.Uint32(hdr[8:12]),
	}
	f.Payload = make([]byte, plen)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return nil, fmt.Errorf("protocol: 读取载荷失败: %w", err)
	}
	return f, nil
}

// WriteFrame 向流写入一帧
func WriteFrame(w io.Writer, f *Frame) error {
	if len(f.Payload) > MaxTCPPayload {
		return ErrTooLarge
	}
	_, err := w.Write(f.Encode())
	return err
}
