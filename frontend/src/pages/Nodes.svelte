<script>
  import { onMount, onDestroy } from 'svelte';
  import StatusTag from '../components/StatusTag.svelte';

  let nodes = [];
  let loading = true;
  let error = '';

  let installCommand = '';
  let server = '';
  let hasBinary = true;
  let enrollError = '';
  let copied = false;
  let rotating = false;

  async function fetchNodes() {
    try {
      const res = await fetch('/api/nodes/json');
      if (!res.ok) throw new Error('加载失败');
      const data = await res.json();
      nodes = data.nodes || [];
      error = '';
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  // 拉取安装命令（首次会在后端生成准入 token）。
  async function fetchEnroll() {
    try {
      const res = await fetch('/api/nodes/enroll', { method: 'POST' });
      if (!res.ok) throw new Error('获取安装命令失败');
      const data = await res.json();
      installCommand = data.install_command || '';
      server = data.server || '';
      hasBinary = data.has_binary !== false;
      enrollError = '';
    } catch (err) {
      enrollError = err.message;
    }
  }

  let timer;
  onMount(() => {
    fetchNodes();
    fetchEnroll();
    timer = setInterval(fetchNodes, 5000);
  });
  onDestroy(() => clearInterval(timer));

  function copyInstall() {
    if (!installCommand) return;
    navigator.clipboard?.writeText(installCommand);
    copied = true;
    setTimeout(() => (copied = false), 1500);
  }

  // 轮换 token：撤销旧安装命令（已在线的 agent 不受影响）。轮换后刷新命令。
  async function rotateToken() {
    if (!confirm('轮换 token 会让旧的安装命令失效（已在线的节点不受影响）。继续？')) return;
    rotating = true;
    try {
      const res = await fetch('/api/nodes/token/rotate', { method: 'POST' });
      if (!res.ok) throw new Error('轮换失败');
      await fetchEnroll();
    } catch (err) {
      enrollError = err.message;
    } finally {
      rotating = false;
    }
  }

  async function removeNode(node) {
    if (node.kind === 'local') return;
    if (!confirm(`删除节点「${node.name}」？如果它还在线，重连后会重新登记；应先在该 VPS 上停止 pfagent 服务。`)) return;
    try {
      const res = await fetch('/api/nodes/delete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ node_id: node.node_id }),
      });
      if (!res.ok) throw new Error('删除失败');
      await fetchNodes();
    } catch (err) {
      error = err.message;
    }
  }

  function fmtBytes(n) {
    if (!n) return '—';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
  }

  function fmtSeen(ts) {
    if (!ts) return '—';
    const d = new Date(ts);
    if (isNaN(d)) return '—';
    const diff = (Date.now() - d.getTime()) / 1000;
    if (diff < 60) return '刚刚';
    if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
    if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
    return d.toLocaleString();
  }
</script>

<div class="page-header">
  <div>
    <h1>节点</h1>
    <p class="muted">本机 WARP 出口 + 各地区远程 agent 出口。在其他 VPS 一行命令即可接入，用它本机 IP 作为出口。</p>
  </div>
</div>

<div class="enroll-card">
  <div class="enroll-head">
    <strong>接入新节点</strong>
    <span class="muted">在目标 VPS（root）上执行下面这行，它会装成 systemd 服务常驻自启</span>
  </div>
  {#if enrollError}
    <div class="banner error">{enrollError}</div>
  {/if}
  {#if !hasBinary}
    <div class="banner warn">
      当前主控未内嵌 agent 二进制，下载会失败。请在构建时先交叉编译（见 scripts/build-agent.sh），Docker 部署会自动处理。
    </div>
  {/if}
  <div class="cmd-row">
    <code class="cmd">{installCommand || '生成中…'}</code>
    <button class="btn-primary" on:click={copyInstall} disabled={!installCommand}>
      {copied ? '已复制' : '复制'}
    </button>
  </div>
  <div class="enroll-foot">
    <span class="muted">主控地址：{server || '—'}</span>
    <button class="link-btn" on:click={rotateToken} disabled={rotating}>
      {rotating ? '轮换中…' : '轮换 token（撤销旧命令）'}
    </button>
  </div>
</div>

{#if error}
  <div class="banner error">{error}</div>
{/if}

{#if loading}
  <div class="loading">加载中…</div>
{:else}
  <div class="table-wrap">
    <table>
      <thead>
        <tr>
          <th>节点</th>
          <th>类型</th>
          <th>出口 IP</th>
          <th>地区</th>
          <th>延迟</th>
          <th>流量 (↑/↓)</th>
          <th>状态</th>
          <th>最近在线</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#each nodes as node}
          <tr>
            <td>{node.name}</td>
            <td>{node.kind === 'local' ? '本机' : 'agent'}</td>
            <td class="mono">{node.public_ip || '—'}</td>
            <td>{node.country || '—'}{node.colo ? ` / ${node.colo}` : ''}</td>
            <td>{node.latency_ms ? node.latency_ms + ' ms' : '—'}</td>
            <td class="mono">{fmtBytes(node.tx_bytes)} / {fmtBytes(node.rx_bytes)}</td>
            <td><StatusTag status={node.online ? 'active' : 'inactive'} /></td>
            <td class="muted">{node.online ? '在线' : fmtSeen(node.last_seen)}</td>
            <td>
              {#if node.kind !== 'local'}
                <button class="icon-btn danger" title="删除节点" on:click={() => removeNode(node)}>删除</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<style>
  .enroll-card {
    background: var(--surface, #fff);
    border: 1px solid var(--border, #e5e7eb);
    border-radius: 10px;
    padding: 16px;
    margin-bottom: 20px;
  }
  .enroll-head {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-bottom: 12px;
  }
  .cmd-row {
    display: flex;
    gap: 10px;
    align-items: stretch;
  }
  .cmd {
    flex: 1;
    display: block;
    padding: 10px 12px;
    background: var(--code-bg, #0f172a);
    color: var(--code-fg, #e2e8f0);
    border-radius: 8px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 13px;
    overflow-x: auto;
    white-space: nowrap;
  }
  .enroll-foot {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-top: 10px;
  }
  .link-btn {
    background: none;
    border: none;
    color: var(--accent, #6366f1);
    cursor: pointer;
    font-size: 13px;
    padding: 0;
  }
  .link-btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .icon-btn.danger {
    color: var(--danger, #ef4444);
  }
  .banner.warn {
    background: #fef3c7;
    color: #92400e;
    padding: 10px 12px;
    border-radius: 8px;
    margin-bottom: 12px;
  }
</style>
