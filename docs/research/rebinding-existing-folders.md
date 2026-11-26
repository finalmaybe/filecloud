# 解除同步后重新使用非空目录：Nextcloud 与 Seafile

场景：用户主动解除一个普通双向同步关系，本地文件保留，然后想把原目录重新连接到同一个远端目录或资料库。这里讨论传统桌面同步，不含按需挂载，也不把暂停后继续当成重新绑定。

## Nextcloud Desktop

- **账号仍在，重新添加文件夹连接**：v35.0.3 的[目录向导](https://github.com/nextcloud/desktop/blob/v35.0.3/src/gui/folderwizard.cpp#L119-L145)调用[路径校验](https://github.com/nextcloud/desktop/blob/v35.0.3/src/gui/folderman.cpp#L1976-L2097)。它检查可写性、已有同步日志和目录重叠，没有要求目录内不能有普通文件。因此原连接正常移除后，可以在设置中通过「Add Folder Sync Connection」选择原目录。[官方客户端使用说明](https://docs.nextcloud.com/server/stable/user_manual/en/desktop/usage.html)
- **重新添加整个账号是不同入口**：旧流程在[官方 FAQ](https://docs.nextcloud.com/server/stable/user_manual/zh_CN/desktop/faq.html)里写作「保留已有数据」或「从头同步」，并说明匹配的文件不需要重新下载。v35.0.3 的[账号向导实现](https://github.com/nextcloud/desktop/blob/v35.0.3/src/gui/wizard/accountwizardcontroller.cpp#L1494-L1506)在常规手动选择非空目录时会报 `Please choose an empty local sync folder.`，不能把 FAQ 的按钮当成各入口通用的操作说明。
- **重新比对**：普通文件在本地和服务端都存在时，[发现阶段的冲突判定](https://github.com/nextcloud/desktop/blob/v35.0.3/src/libsync/discovery.cpp#L1894-L1934)会使用大小、修改时间等信息。对判定为冲突的文件，[官方冲突说明](https://docs.nextcloud.com/server/stable/user_manual/en/desktop/conflicts.html)描述为：服务端版本同步到原路径，本地编辑保留为冲突副本，供比较整理。

## Seafile Sync Client

- [解除同步与重新同步](https://cloud.seafile.com/published/seafile-user-manual/syncing_client/syncing_librarise.md)明确将「和已有文件夹同步」列为解绑后重新连接的方法。[同步已有文件夹](https://help.seafile.com/syncing_client/syncing_existing_folders/)给出的路径是右键资料库 → 同步该资料库 → 和已有文件夹同步 → 选择目录；手册示例要求本地目录与资料库同名。
- 手册规定两边文件合并，遇到同路径、内容不同的文件会产生冲突副本。v9.0.22 的[目录确认实现](https://github.com/haiwen/seafile-client/blob/v9.0.22/src/ui/download-repo-dialog.cpp#L296-L339)也明确提供「合并内容」和「换新目录」两种选择。

## 对 Filecloud 的启示

「复用已有文件」与「传播删除」是两项决定。有共同版本时可区分新增和删除；缺少共同版本时，仅在一边存在的文件有歧义。可先支持两端一致时原地重绑，再把有差异的情况交给显式核对：预览要上传、下载和保留为冲突副本的项目，不把单边缺失自动解释为删除。此段是产品建议，以上游文档和源码为参考，未对两个客户端做本机实测。
