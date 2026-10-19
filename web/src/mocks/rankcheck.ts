// mock 的正则检查（真实校验在服务端 internal/rank）
export function patternOK(p: string): boolean {
  if (!p || /\(\?(?!:)/.test(p) || /\\[AzQECpP]|\\x\{|\[\[:/.test(p)) return false
  try {
    new RegExp(p, "i")
    return true
  } catch {
    return false
  }
}
