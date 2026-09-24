//go:build !windows && !darwin && !linux

package platform

import (
	"errors"
	"os"
	"path/filepath"
)

// unixGenericPlatform 其它 OS（freebsd 等）的降级实现：路径按 TODO 兜底，
// 系统动作返回"不支持"（保持包可编译、行为显式失败）。
// 本文件仅保证跨平台编译完整性，非交付目标。
type unixGenericPlatform struct {
	machine
}

var _ Platform = (*unixGenericPlatform)(nil)

func newPlatform() *unixGenericPlatform {
	p := &unixGenericPlatform{}
	data := "/var/ddns-hosts-sync"
	p.machine = machine{
		paths: pathSet{
			hosts:   "/etc/hosts",
			dataDir: data,
			config:  filepath.Join(data, "config", "config.yaml"),
			state:   filepath.Join(data, "state"),
			log:     filepath.Join(data, "logs", "sync.log"),
		},
		exec: execPath(),
	}
	return p
}

func (p *unixGenericPlatform) FlushDNSCache() error {
	return errors.New("platform: 当前平台不支持 DNS 缓存刷新")
}

// SetupAllDirs 创建数据目录树（S14 修正：需建 dataDir 本身与 config/state/
// logs 子目录，此前实现误用 filepath.Dir(config) 导致形状不一致）。
func (p *unixGenericPlatform) SetupAllDirs() error {
	for _, d := range dataDirTree(p.paths.dataDir, p.paths.state) {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (p *unixGenericPlatform) InstallTrayAutostart() error {
	return errors.New("platform: 当前平台不支持托盘自启")
}

func (p *unixGenericPlatform) RemoveTrayAutostart() error { return nil }

func (p *unixGenericPlatform) IsAdmin() bool { return os.Geteuid() == 0 }
