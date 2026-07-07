export function fmtBytes(value) {
  let n = Number(value || 0);
  if (n < 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return n.toFixed(i === 0 ? 0 : 2) + ' ' + units[i];
}

export function fmtBps(value) {
  return fmtBytes(value) + '/s';
}

export function fmtTime(value, empty) {
  if (!value || String(value).startsWith('0001-')) return empty;
  return new Date(value).toLocaleString();
}

export function metric(value, unit) {
  if (value === null || value === undefined || value === '' || Number(value) === 0) return '检测中';
  return String(value) + (unit || '');
}

export function prettyError(value) {
  if (!value) return '';
  const text = String(value);
  if (text.includes('出口 IP 漂移')) return text.replace('出口 IP 漂移: ', '出口 IP 漂移：');
  if (text.includes('实时探测失败')) return text.replace('实时探测失败: ', '实时探测失败：');
  if (text.includes('no healthy unique WARP account available')) return '正在等待新的唯一健康节点';
  if (text.includes('Proxy Authentication Required') || text.includes('HTTP 407')) return '隧道未就绪，后台会自动重绑';
  if (text.includes('i/o timeout')) return 'WARP 隧道超时，后台会自动筛掉';
  if (text.includes('context deadline exceeded')) return '连接超时，后台会自动重试';
  if (text.includes('429') || text.includes('1015')) return 'Cloudflare 注册限速，稍后会自动继续';
  return text;
}
