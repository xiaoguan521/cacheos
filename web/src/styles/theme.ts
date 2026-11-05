// 设计令牌：颜色只用于表达状态（规格书 7 · 控制台）
export const palette = {
  ink: '#16323A', // 侧栏、深色文字
  signal: '#0E7C86', // 主色：可操作、选中
  paper: '#F3F5F4', // 页面背景
  go: '#2F9E5B', // 在线、成功
  lane: '#D9A21B', // 进行中、待处理
  fault: '#C8423B', // 故障、回滚
  muted: '#6B7C80',
}

export const fontFamily =
  '"PingFang SC","HarmonyOS Sans SC","Microsoft YaHei","Noto Sans CJK SC","Source Han Sans SC",system-ui,-apple-system,"Segoe UI",sans-serif'

export const theme = {
  token: {
    colorPrimary: palette.signal,
    colorSuccess: palette.go,
    colorWarning: palette.lane,
    colorError: palette.fault,
    colorInfo: palette.signal,
    colorBgLayout: palette.paper,
    colorTextBase: '#1F2A2E',
    borderRadius: 6,
    fontFamily,
    fontSize: 14,
  },
  components: {
    Layout: { siderBg: palette.ink, headerBg: '#FFFFFF', bodyBg: palette.paper },
    Menu: {
      darkItemBg: palette.ink,
      darkSubMenuItemBg: palette.ink,
      darkItemSelectedBg: '#1F4650',
      darkItemColor: '#B9C9CC',
      darkItemSelectedColor: '#FFFFFF',
    },
  },
}
