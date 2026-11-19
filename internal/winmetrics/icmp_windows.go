//go:build windows

package winmetrics

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

const (
	rttPacketsPerRound = 3
	rttPacketTimeout   = time.Second
)

func probeTarget(parent context.Context, target string, now time.Time) RTTTarget {
	out := RTTTarget{Target: target, Display: displayTarget(target), State: StatePending, TS: now.UnixMilli()}
	resolveCtx, cancel := context.WithTimeout(parent, 3*time.Second)
	ips, err := net.DefaultResolver.LookupIPAddr(resolveCtx, target)
	cancel()
	if err != nil {
		out.State = StateDNSError
		out.Message = "域名解析失败"
		return out
	}

	// Prefer the IPv4 route of a dual-stack host; IPv6-only names and numeric
	// IPv6 addresses use the matching Windows echo function.
	var v4 net.IP
	var v6 net.IPAddr
	for _, ip := range ips {
		if x := ip.IP.To4(); x != nil {
			v4 = x
			break
		}
		if v6.IP == nil && ip.IP.To16() != nil {
			v6 = ip
		}
	}
	if v4 == nil && v6.IP == nil {
		out.State = StateDNSError
		out.Message = "未解析到可用地址"
		return out
	}

	useIPv6 := v4 == nil
	h, err := createIcmpFile(useIPv6)
	if err != nil {
		out.State = StateError
		out.Message = "无法启动 ICMP 探测"
		if isPermissionError(err) {
			out.State = StatePermissionError
			out.Message = "ICMP 被策略或权限限制"
		}
		return out
	}
	defer closeIcmpFile(h)

	payload := []byte("DeskPulse Agent")
	var send func() (uint32, uint32, error)
	timeout := uint32(rttPacketTimeout.Milliseconds())
	if useIPv6 {
		dst := syscall.RawSockaddrInet6{Family: syscall.AF_INET6}
		copy(dst.Addr[:], v6.IP.To16())
		if v6.Zone != "" {
			iface, zoneErr := net.InterfaceByName(v6.Zone)
			if zoneErr != nil {
				out.State = StateError
				out.Message = "无法识别 IPv6 区域接口"
				return out
			}
			dst.Scope_id = uint32(iface.Index)
		}
		send = func() (uint32, uint32, error) { return sendIcmp6(h, dst, payload, timeout) }
	} else {
		dst := binary.LittleEndian.Uint32(v4)
		send = func() (uint32, uint32, error) { return sendIcmp(h, dst, payload, timeout) }
	}
	var rtts []float64
	var lastErr error
	for i := 0; i < rttPacketsPerRound && parent.Err() == nil; i++ {
		status, rtt, sendErr := send()
		if isPermissionError(sendErr) {
			out.State = StatePermissionError
			out.Message = "ICMP 被策略或权限限制"
			return out
		}
		out.Sent++
		if sendErr != nil {
			lastErr = sendErr
			continue
		}
		switch status {
		case icmpSuccess:
			rtts = append(rtts, float64(rtt))
		case icmpRequestTimedOut, icmpDestinationHostUnreach, icmpDestinationNetUnreachable, icmpTTLExpiredTransit, icmpGeneralFailure:
			// Lost echo; continue probing.
		default:
			lastErr = fmt.Errorf("ICMP 状态 %d", status)
		}
	}
	out.Recv = len(rtts)
	out.Loss = lossPercent(out.Sent, out.Recv)
	out.AvgMS = averageMS(rtts)
	out.MaxMS = maxMS(rtts)
	switch {
	case out.Sent == 0:
		out.State = StateError
		out.Message = "没有发出探测包"
	case out.Recv == 0:
		out.State = StateUnreachable
		out.Message = "全部丢包或超时"
		if lastErr != nil {
			out.Message = "全部探测失败或超时"
		}
	case out.Recv < out.Sent:
		out.State = StatePartial
		out.Message = "部分丢包"
	default:
		out.State = StateOK
	}
	return out
}

func displayTarget(target string) string {
	if strings.EqualFold(strings.TrimSpace(target), "www.baidu.com") {
		return "百度"
	}
	return target
}
