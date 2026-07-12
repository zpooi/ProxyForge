// Package agentproto 定义主控与远程 agent 之间在 yamux 流上的最小握手协议。
//
// 它刻意只依赖标准库，供主控（agenthub）和轻量 agent（cmd/pfagent）共享，
// 避免协议层反向依赖具体的 WARP 隧道实现。
package agentproto

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	// ProtocolVersion 用于将来演进握手；当前 agent 与主控必须一致。
	ProtocolVersion = 1

	// 主控在每条 yamux 流开头写入目标地址，agent 本地拨号后回写 1 字节状态。
	// 状态字节让 DialContext 能感知远端拨号失败，从而在客户端层触发故障转移。
	DialOK   = 0x00
	DialFail = 0x01

	// 目标地址（host:port）长度上限。域名最长 253，加端口约 260，留足余量。
	maxTargetLen = 512
)

// WriteTarget 把目标地址以 2 字节大端长度前缀写入流。
func WriteTarget(w io.Writer, target string) error {
	if len(target) == 0 || len(target) > maxTargetLen {
		return fmt.Errorf("agentproto: invalid target length %d", len(target))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(target)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := io.WriteString(w, target)
	return err
}

// ReadTarget 读取由 WriteTarget 写入的目标地址。
func ReadTarget(r io.Reader) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	if n == 0 || int(n) > maxTargetLen {
		return "", fmt.Errorf("agentproto: invalid target length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// WriteStatus 回写 1 字节拨号状态。
func WriteStatus(w io.Writer, ok bool) error {
	b := byte(DialFail)
	if ok {
		b = DialOK
	}
	_, err := w.Write([]byte{b})
	return err
}

// ReadStatus 读取 1 字节拨号状态。
func ReadStatus(r io.Reader) (bool, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return false, err
	}
	return b[0] == DialOK, nil
}
