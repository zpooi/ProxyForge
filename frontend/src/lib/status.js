export function tagLabel(status) {
  return {
    active: '可用',
    disabled: '停用',
    error: '错误',
    running: '运行中',
    repairing: '观察中',
    failed: '自动修复中',
    waiting: '等待节点',
    probing: '检测中',
  }[status || ''] || status || '-';
}

export function slotState(slot) {
  if (!slot.account_tag) return 'waiting';
  const failures = Math.max(Number(slot.probe_failures || 0), Number(slot.ip_drift_failures || 0));
  if (slot.last_error && failures < 5) return 'repairing';
  if (slot.last_error) return 'failed';
  if (!slot.pinned_public_ip && !slot.public_ip) return 'probing';
  return slot.status || 'active';
}
