# 文件预览交互参考

对照 Nextcloud Viewer `stable32` 的固定提交 `429b3a8`：

- [Viewer.vue：查看器外壳](https://github.com/nextcloud/viewer/blob/429b3a8b32e36dc87bb2f2f4268f86e74d79fba7/src/views/Viewer.vue#L30-L99) 将文件名、关闭、前后切换和下载放在同一模态查看器里。[文件列表逻辑](https://github.com/nextcloud/viewer/blob/429b3a8b32e36dc87bb2f2f4268f86e74d79fba7/src/views/Viewer.vue#L736-L790) 优先沿用调用方传入的列表。
- [Images.vue：缩放与手势](https://github.com/nextcloud/viewer/blob/429b3a8b32e36dc87bb2f2f4268f86e74d79fba7/src/components/Images.vue#L287-L318) 将放大查看细节与滑动切换图片分开处理。

Filecloud 采用同样的打开、浏览、关闭流程。切换范围限定为打开时目录中已加载、符合当前筛选的文件，沿用原顺序和固定提交；不会为一次预览遍历整个资料库。图片默认适应窗口，支持逐级缩放和查看原始尺寸。放大后拖动图片查看细节，全图状态下左右滑动切换文件。独立链接与不支持预览时的下载入口保留；版本操作仍在文件菜单中。
