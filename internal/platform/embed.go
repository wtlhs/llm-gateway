package platform

import (
	"embed"
	"io/fs"
)

//go:embed all:web_dist
var webDist embed.FS

// distFS 返回前端构建产物(web/dist)的子文件系统。
// 若目录不存在(开发模式未构建前端), 返回错误, 调用方降级为提示信息。
func distFS() (fs.FS, error) {
	sub, err := fs.Sub(webDist, "web_dist")
	if err != nil {
		return nil, err
	}
	// 检查 index.html 是否存在(判断是否有构建产物)
	entries, err := fs.ReadDir(sub, ".")
	if err != nil || len(entries) == 0 {
		return nil, fs.ErrNotExist
	}
	return sub, nil
}
