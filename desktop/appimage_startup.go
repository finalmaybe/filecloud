package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type loginStartup interface {
	IsEnabled() (bool, error)
	Enable() error
	Disable() error
}

type imageStartup struct{ image, entry string }

func (s imageStartup) Enable() error {
	if strings.IndexFunc(s.image, unicode.IsControl) >= 0 {
		return errors.New("AppImage 路径含控制字符，请移动文件后再开启登录启动。")
	}
	if err := os.MkdirAll(filepath.Dir(s.entry), 0700); err != nil {
		return err
	}
	return os.WriteFile(s.entry, []byte(s.desktopEntry()), 0600)
}

func (s imageStartup) IsEnabled() (bool, error) {
	data, err := os.ReadFile(s.entry)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return string(data) == s.desktopEntry(), err
}

func (s imageStartup) Disable() error {
	err := os.Remove(s.entry)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s imageStartup) desktopEntry() string {
	// Desktop Entry string escapes are decoded before Exec argument quoting and field codes.
	arg := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`", "%", "%%").Replace(s.image)
	return "[Desktop Entry]\nType=Application\nName=Filecloud\nTerminal=false\nExec=\"" + strings.ReplaceAll(arg, `\`, `\\`) + "\"\n"
}
