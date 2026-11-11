// 把 agent 返回的回复结构（reply）格式化为终端式的行列表，供 Web 命令行输出区展示。

export interface ReplyNode {
  type: 'simple' | 'error' | 'integer' | 'bulk' | 'null' | 'array' | string
  value?: unknown
  encoding?: string
}

export interface ReplyLine {
  text: string
  tone: 'normal' | 'error' | 'muted'
}

export function formatReply(node: ReplyNode | null | undefined): ReplyLine[] {
  if (!node) return [{ text: '(nil)', tone: 'muted' }]
  switch (node.type) {
    case 'simple':
      return [{ text: String(node.value ?? ''), tone: 'normal' }]
    case 'error':
      return [{ text: `(error) ${String(node.value ?? '')}`, tone: 'error' }]
    case 'integer':
      return [{ text: `(integer) ${String(node.value)}`, tone: 'normal' }]
    case 'bulk':
      if (node.encoding === 'base64') return [{ text: `(base64) ${String(node.value)}`, tone: 'normal' }]
      return [{ text: JSON.stringify(node.value ?? ''), tone: 'normal' }]
    case 'null':
      return [{ text: '(nil)', tone: 'muted' }]
    case 'array': {
      const items = Array.isArray(node.value) ? (node.value as ReplyNode[]) : []
      if (items.length === 0) return [{ text: '(empty array)', tone: 'muted' }]
      const lines: ReplyLine[] = []
      items.forEach((item, i) => {
        formatReply(item).forEach((l, j) => {
          lines.push({ text: `${j === 0 ? `${i + 1}) ` : '   '}${l.text}`, tone: l.tone })
        })
      })
      return lines
    }
    default:
      return [{ text: `(unknown) ${JSON.stringify(node.value ?? null)}`, tone: 'muted' }]
  }
}
