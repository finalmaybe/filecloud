// Package syncignore defines the device-local name rules shared by Desktop and its sync client.
package syncignore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

const Env = "FILECLOUD_SYNC_IGNORE_FILE"

type Rules struct {
	SkipHidden bool
	Patterns   []string
}

func Load(filename string) (Rules, error) {
	if filename == "" {
		return Rules{}, nil
	}
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return Rules{}, nil
	}
	if err != nil {
		return Rules{}, errors.New("无法读取同步忽略规则，请检查配置文件权限。")
	}
	var r Rules
	if err := json.Unmarshal(data, &r); err != nil {
		return Rules{}, errors.New("同步忽略规则的配置文件格式有误。")
	}
	return r, r.Validate()
}

func (r Rules) Validate() error {
	for i, p := range r.Patterns {
		if strings.TrimSpace(p) == "" || strings.ContainsAny(p, "/\\\x00\r\n") {
			return fmt.Errorf("第 %d 条忽略规则无效：请填写文件或文件夹名称，不要填写路径。", i+1)
		}
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("第 %d 条忽略规则的通配符格式有误，例如可用 *.tmp。", i+1)
		}
	}
	return nil
}

func (r Rules) Active() bool { return r.SkipHidden || len(r.Patterns) != 0 }

// Match reports whether a single file or directory name is ignored, at any depth.
func (r Rules) Match(name string) bool {
	if r.SkipHidden && strings.HasPrefix(name, ".") {
		return true
	}
	for _, p := range r.Patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}
