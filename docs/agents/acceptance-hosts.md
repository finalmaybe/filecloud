# 跨平台验收主机

以下命令从开发机的 Filecloud 仓库根目录执行。先通过 SSH 查询系统版本，确认连到目标系统，再按需运行完整验收。

## Windows：本机 Docker 中的 Windows 虚拟机

容器名为 `windows`，Windows 来宾的 SSH 端口映射到开发机的 `127.0.0.1:2222`。用容器配置的用户名通过 SSH 登录；`docker exec` 只用于读取用户名，测试要在 Windows 里跑。

```sh
docker port windows 22/tcp
win_user=$(docker exec windows printenv USERNAME)
ssh -o BatchMode=yes -p 2222 -l "$win_user" 127.0.0.1 'cmd.exe /c ver'
```

要进入交互会话，去掉最后的 `'cmd.exe /c ver'`。将待测源码放在 Windows 本地固定 NTFS 卷的短路径下，例如 `C:\fc`；过长的路径会让 junction 测试在创建目录时失败。

## macOS：局域网 Mac

Mac 的 SSH 目标保存在开发机的本仓库 Git 本地配置中。更换开发机时，先执行 `git config --local filecloud.macosHost '<SSH 用户名>@<Mac 的局域网地址>'`，把占位符换成实际值。该配置不随仓库提交。

```sh
mac_host=$(git config --local --get filecloud.macosHost)
ssh -o BatchMode=yes "$mac_host" sw_vers -productVersion
```

用 `ssh "$mac_host"` 进入交互会话，将待测源码放在 Mac 的本地 APFS 卷上。

## 运行验收

两个系统使用同一份待测源码。在**目标系统的仓库根目录**，按 [README 的 1B 验收命令](../../README.md#验证)运行 `TestCrossPlatformAcceptanceMatrix`：macOS 使用 shell 版本，Windows 使用 PowerShell 版本。验收范围、通过条件和历次实测结果见[跨平台可执行门禁](../design/acceptance-tests.md#跨平台可执行门禁)。
