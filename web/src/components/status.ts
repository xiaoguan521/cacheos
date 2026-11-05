// 发布与节点状态的中文名和语义色
export const deployStatus: Record<string, { text: string; color: string }> = {
  APPLYING: { text: '发布中', color: 'gold' },
  APPLIED: { text: '已生效', color: 'green' },
  PARTIAL: { text: '部分生效', color: 'orange' },
  FAILED: { text: '未生效', color: 'red' },
  PENDING_NODES: { text: '等待节点上线', color: 'default' },
}

export const nodeResult: Record<string, { text: string; color: string }> = {
  PENDING: { text: '排队中', color: 'default' },
  APPLYING: { text: '执行中', color: 'gold' },
  APPLIED: { text: '已生效', color: 'green' },
  INVALID: { text: '校验未通过', color: 'red' },
  ROLLED_BACK: { text: '已自动回滚', color: 'volcano' },
  DEGRADED: { text: '回滚失败', color: 'red' },
  REJECTED: { text: '已拒绝', color: 'red' },
  CONFLICT: { text: '版本冲突', color: 'orange' },
  OFFLINE: { text: '离线', color: 'default' },
  TIMEOUT: { text: '超时', color: 'red' },
}

export const kindName: Record<string, string> = { publish: '发布', rollback: '回滚', align: '自动对齐', cert_renew: '证书续期', restore: '漂移恢复' }
