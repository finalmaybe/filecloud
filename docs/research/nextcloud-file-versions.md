# 文件版本栏的交互参考

- [Nextcloud 用户手册：Version control](https://docs.nextcloud.com/server/latest/user_manual/en/files/version_control.html)：版本入口位于文件详情侧栏的 Versions 标签。Filecloud 沿用文件列表里的「… → 历史版本」，直接打开对应文件的版本栏。
- [FilesVersionsSidebarTab.vue](https://github.com/nextcloud/server/blob/master/apps/files_versions/src/views/FilesVersionsSidebarTab.vue) 将当前版本放在最前面，并在切换文件时载入其记录。[VersionEntry.vue](https://github.com/nextcloud/server/blob/master/apps/files_versions/src/components/VersionEntry.vue) 将时间和对单一版本的操作放在同一条目里。
- Filecloud 的「当前版本」固定为打开文件列表时所选的快照；变更记录按主线顺序而非日期排序。网页只读，可就地查看旧版本的元数据，遵循[服务端 Web 控制台契约](../design/web-console.md)。
