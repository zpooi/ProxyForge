// Package agentdist 内嵌预编译的 pfagent 二进制，供主控通过 HTTP 分发。
//
// 部署路径（Dockerfile）会在编译主控前先交叉编译各架构的 agent 放进 dist/，
// go:embed 把它们打进主控二进制，运行时零外部依赖即可下发安装脚本 + 二进制。
// 未构建 agent 时 dist/ 只有 README，下载接口返回友好提示而非编译失败。
package agentdist

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed dist/*
var distFS embed.FS

// Binary 描述一个可下发的 agent 构建产物。
type Binary struct {
	OS   string // linux
	Arch string // amd64 / arm64
	Name string // dist 下的文件名，如 pfagent-linux-amd64
}

// filePrefix 是构建产物的统一命名前缀。scripts/build-agent.* 按此命名输出。
const filePrefix = "pfagent-"

// List 返回当前内嵌的所有 agent 构建产物（按 os/arch 排序，稳定）。
func List() []Binary {
	entries, err := fs.ReadDir(distFS, "dist")
	if err != nil {
		return nil
	}
	var out []Binary
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), filePrefix) {
			continue
		}
		// 形如 pfagent-linux-amd64 / pfagent-linux-arm64。
		parts := strings.Split(strings.TrimSuffix(e.Name(), ".exe"), "-")
		if len(parts) != 3 {
			continue
		}
		out = append(out, Binary{OS: parts[1], Arch: parts[2], Name: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS != out[j].OS {
			return out[i].OS < out[j].OS
		}
		return out[i].Arch < out[j].Arch
	})
	return out
}

// HasAny 报告是否内嵌了至少一个 agent 二进制。
func HasAny() bool { return len(List()) > 0 }

// Read 返回指定 os/arch 的 agent 二进制内容。找不到时返回错误。
func Read(goos, goarch string) ([]byte, string, error) {
	name := filePrefix + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	b, err := distFS.ReadFile("dist/" + name)
	if err != nil {
		return nil, "", fmt.Errorf("agent 二进制 %s 未内嵌（部署时需先交叉编译，见 scripts/build-agent.sh）", name)
	}
	return b, name, nil
}
