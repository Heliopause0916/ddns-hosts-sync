package model

import "fmt"

// IPOrder 期望块的 IP 排序语义（DSD §2.2/§5.3）。
//
// 与 DNSConfig.IPVersion 取值一致（ipv4/ipv6/both），由任务端在装配合法解析
// 结果到期望块时传入 BuildBlock。排序规则本身固定为 §5.3：IPv4 字典序在前、
// IPv6 字典序在后（both 模式下 A 行恒在 AAAA 行前），此枚举仅保留 DSD 签名
// 中的上下文入参。
type IPOrder string

const (
	// OrderIPv4 仅 IPv4（ip_version=ipv4）。
	OrderIPv4 IPOrder = "ipv4"
	// OrderIPv6 仅 IPv6（ip_version=ipv6）。
	OrderIPv6 IPOrder = "ipv6"
	// OrderBoth 两者皆可（ip_version=both）。
	OrderBoth IPOrder = "both"
)

// ParseIPOrder 将 DNSConfig.IPVersion 字符串映射为 IPOrder；非法值回落 OrderBoth
// 并在第二个返回值报非 nil（调用方按规则 8 记 warning）。
func ParseIPOrder(s string) (IPOrder, error) {
	switch IPOrder(s) {
	case OrderIPv4:
		return OrderIPv4, nil
	case OrderIPv6:
		return OrderIPv6, nil
	case OrderBoth:
		return OrderBoth, nil
	default:
		return OrderBoth, fmt.Errorf("非法 ip_version %q，回落 both", s)
	}
}
