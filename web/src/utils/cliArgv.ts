// Web 命令行的命令行解析：把输入文本拆成 argv 数组（不经过 shell，只做词法切分）。
//
// 规则（与常见 shell 的词法一致，但没有变量展开、通配与管道）：
//   - 空白（空格、制表符、换行）分隔参数；多个空白视为一个。
//   - 反斜杠在引号外转义下一个字符；行尾单独的反斜杠是错误。
//   - 双引号内可包含空白；双引号内只有 \" 与 \\ 被转义，其余反斜杠保持原样。
//   - 单引号内原样保留，直到下一个单引号；单引号内不做转义。
//   - "" 与 '' 产生一个空参数。引号未闭合是错误。

export class CliParseError extends Error {}

const WHITESPACE = new Set([' ', '\t', '\n', '\r'])

export function parseCommandLine(input: string): string[] {
  const argv: string[] = []
  let cur = ''
  let inToken = false
  let i = 0
  const n = input.length

  while (i < n) {
    const ch = input[i]
    if (WHITESPACE.has(ch)) {
      if (inToken) {
        argv.push(cur)
        cur = ''
        inToken = false
      }
      i++
      continue
    }
    if (ch === '\\') {
      if (i + 1 >= n) throw new CliParseError('行尾有多余的反斜杠')
      cur += input[i + 1]
      inToken = true
      i += 2
      continue
    }
    if (ch === '"') {
      inToken = true
      i++
      let closed = false
      while (i < n) {
        const c = input[i]
        if (c === '"') {
          closed = true
          i++
          break
        }
        if (c === '\\' && i + 1 < n && (input[i + 1] === '"' || input[i + 1] === '\\')) {
          cur += input[i + 1]
          i += 2
          continue
        }
        cur += c
        i++
      }
      if (!closed) throw new CliParseError('双引号未闭合')
      continue
    }
    if (ch === "'") {
      inToken = true
      i++
      const end = input.indexOf("'", i)
      if (end < 0) throw new CliParseError('单引号未闭合')
      cur += input.slice(i, end)
      i = end + 1
      continue
    }
    cur += ch
    inToken = true
    i++
  }
  if (inToken) argv.push(cur)
  return argv
}
