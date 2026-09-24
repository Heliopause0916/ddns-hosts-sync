//go:build !windows && !darwin && !linux

package tasks

import "fmt"

// unsupportedManager 其它 OS 的降级实现：IsSupported=false，install 子命令
// 转而为"平台不支持计划任务"提示（DSD §2.5）。
type unsupportedManager struct{}

// New 返回降级实现。
func New() Manager {
	return unsupportedManager{}
}

// IsSupported 常量 false。
func (unsupportedManager) IsSupported() bool { return false }

func (unsupportedManager) Install(spec TaskSpec) error {
	return fmt.Errorf("当前平台不支持计划任务")
}

func (unsupportedManager) Uninstall(name string) error { return nil }

func (unsupportedManager) Trigger(name string) error {
	return fmt.Errorf("当前平台不支持计划任务")
}

func (unsupportedManager) Exists(name string) (bool, error) { return false, nil }
