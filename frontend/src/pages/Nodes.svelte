<script>
  import { onMount, onDestroy } from 'svelte';
  import StatusTag from '../components/StatusTag.svelte';
  import Icon from '../components/Icon.svelte';

  let nodes = [];
  let loading = true;
  let error = '';

  let installCommand = '';
  let uninstallCommand = '';
  let server = '';
  let hasBinary = true;
  let enrollError = '';
  let copied = '';
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
      uninstallCommand = data.uninstall_command || '';
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

  async function writeClipboard(text) {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return;
    }
    const input = document.createElement('textarea');
    input.value = text;
    input.setAttribute('readonly', '');
    input.style.position = 'fixed';
    input.style.left = '-9999px';
    document.body.appendChild(input);
    input.select();
    document.execCommand('copy');
    document.body.removeChild(input);
  }

  async function copyCommand(command, kind) {
    if (!command) return;
    try {
      await writeClipboard(command);
      copied = kind;
      setTimeout(() => {
        if (copied === kind) copied = '';
      }, 1500);
    } catch (err) {
      enrollError = '复制失败：' + err.message;
    }
  }

  function copyInstall() {
    return copyCommand(installCommand, 'install');
  }

  function copyUninstall() {
    return copyCommand(uninstallCommand, 'uninstall');
  }

  // 轮换 token 后，旧命令和已安装 agent 的后续重连都会失效。
  async function rotateToken() {
    if (!confirm('重置后，已有节点重连前需重新安装。继续？')) return;
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
    if (n === null || n === undefined || Number.isNaN(Number(n))) return '—';
    if (Number(n) <= 0) return '0 B';
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

  function displayInstallCommand(value) {
    if (!value) return '生成中…';
    return value.replace(/(token=)([^'&\s]+)/, (_, prefix, token) => {
      if (token.length <= 16) return prefix + token;
      return `${prefix}${token.slice(0, 8)}…${token.slice(-4)}`;
    });
  }
</script>

<div class="page-header compact-header">
  <h2>节点</h2>
</div>

<div class="enroll-card">
  <div class="enroll-toolbar">
    <div class="enroll-head">
      <strong>接入节点</strong><span>自动创建 3 个当地 WARP 出口</span>
    </div>
    <div class="enroll-actions">
      <button class="link-btn uninstall-btn" on:click={copyUninstall} disabled={!uninstallCommand}>
        <Icon name={copied === 'uninstall' ? 'check' : 'copy'} size={15} />
        <span>{copied === 'uninstall' ? '已复制' : '卸载命令'}</span>
      </button>
      <button class="link-btn" on:click={rotateToken} disabled={rotating}>
        {rotating ? '重置中…' : '重置接入码'}
      </button>
    </div>
  </div>
  <div class="enroll-body">
    {#if enrollError}
      <div class="banner error">{enrollError}</div>
    {/if}
    {#if !hasBinary}
      <div class="banner warn">未找到 agent 二进制，请先完成构建。</div>
    {/if}
    <div class="cmd-row">
      <code class="cmd" title={installCommand}>{displayInstallCommand(installCommand)}</code>
      <button
        class="copy-trigger command-copy"
        class:done={copied === 'install'}
        title={copied === 'install' ? '已复制' : '复制命令'}
        aria-label={copied === 'install' ? '已复制' : '复制命令'}
        on:click={copyInstall}
        disabled={!installCommand}
      >
        <Icon name={copied === 'install' ? 'check' : 'copy'} size={18} />
      </button>
    </div>
    <div class="enroll-server">
      <span>主控</span>
      <code>{server || '—'}</code>
    </div>
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
        <tr><th>节点</th><th>出口 IP</th><th>地区</th><th>延迟</th><th>流量 ↑ / ↓</th><th>状态</th><th></th></tr>
      </thead>
      <tbody>
        {#if nodes.length}
          {#each nodes as node}
            <tr>
              <td>
                <div class="node-name">{node.name}</div>
                <div class="node-kind">{node.kind === 'local' ? '本机' : 'Agent WARP'}</div>
              </td>
              <td class="mono">{node.public_ip || '—'}</td>
              <td>{node.country || '—'}{node.colo ? ` / ${node.colo}` : ''}</td>
              <td>{node.latency_ms ? node.latency_ms + ' ms' : '—'}</td>
              <td class="mono">{fmtBytes(node.tx_bytes)} / {fmtBytes(node.rx_bytes)}</td>
              <td>
                <div class="node-status">
                  <StatusTag status={node.online ? 'active' : 'inactive'} />
                  {#if !node.online}<span>{fmtSeen(node.last_seen)}</span>{/if}
                </div>
              </td>
              <td>
                {#if node.kind !== 'local'}
                  <button class="icon-btn danger node-delete" title="删除节点" aria-label="删除节点" on:click={() => removeNode(node)}>
                    <Icon name="delete" size={17} />
                  </button>
                {/if}
              </td>
            </tr>
          {/each}
        {:else}
          <tr><td class="empty-state" colspan="7">暂无节点</td></tr>
        {/if}
      </tbody>
    </table>
  </div>
{/if}

<style>
  .compact-header {
    margin-bottom: 16px;
  }
  .compact-header h2 {
    margin: 0;
  }
  .enroll-card {
    background: var(--surface, #fff);
    border: 1px solid var(--border, #e5e7eb);
    border-radius: 10px;
    margin-bottom: 16px;
    overflow: hidden;
  }
  .enroll-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 14px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .enroll-head {
    display: flex;
    align-items: baseline;
    gap: 9px;
    min-width: 0;
  }
  .enroll-head span {
    color: var(--text-3);
    font-size: 12px;
  }
  .enroll-actions {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-shrink: 0;
  }
  .enroll-body {
    padding: 14px 16px;
  }
  .cmd-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 40px;
    gap: 8px;
    align-items: center;
  }
  .cmd {
    display: block;
    min-width: 0;
    padding: 9px 11px;
    background: var(--code-bg, #0f172a);
    color: var(--code-fg, #e2e8f0);
    border-radius: 8px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .command-copy {
    width: 40px;
    min-height: 38px;
    padding: 8px;
  }
  .enroll-server {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    margin-top: 9px;
    color: var(--text-3);
    font-size: 11px;
  }
  .enroll-server code {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    background: transparent;
    padding: 0;
    color: var(--text-2);
  }
  .link-btn {
    background: none;
    border: none;
    color: var(--accent, #6366f1);
    cursor: pointer;
    font-size: 13px;
    padding: 0;
    min-height: 30px;
  }
  .uninstall-btn {
    gap: 5px;
    color: var(--danger);
  }
  .uninstall-btn:hover {
    background: transparent;
    color: var(--danger-hover);
  }
  .link-btn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .icon-btn.danger {
    color: var(--danger, #ef4444);
  }
  .node-delete {
    min-width: 32px;
    min-height: 32px;
    padding: 6px;
    background: transparent;
    color: var(--text-3);
  }
  .node-delete:hover {
    background: #fdeaea;
    color: var(--danger, #ef4444);
  }
  .node-name {
    color: var(--text);
    font-weight: 600;
  }
  .node-kind {
    margin-top: 1px;
    color: var(--text-3);
    font-size: 11px;
  }
  .node-status {
    display: flex;
    align-items: center;
    gap: 7px;
    color: var(--text-3);
    font-size: 11px;
    white-space: nowrap;
  }
  .empty-state {
    height: 108px;
    color: var(--text-3);
    text-align: center;
  }
  .banner.warn {
    background: #fef3c7;
    color: #92400e;
    padding: 8px 10px;
    border-radius: 8px;
    margin-bottom: 12px;
  }
  .banner.error {
    margin-bottom: 12px;
    padding: 8px 10px;
    border-radius: 8px;
    background: #fdeaea;
    color: var(--danger-hover);
  }
  :global(.table-wrap) {
    border-radius: 10px;
  }
  :global(.table-wrap table) {
    min-width: 760px;
  }
  :global(.table-wrap th),
  :global(.table-wrap td) {
    padding: 10px 12px;
  }
  :global(.table-wrap th:first-child),
  :global(.table-wrap td:first-child) {
    padding-left: 16px;
  }
  :global(.table-wrap th:last-child),
  :global(.table-wrap td:last-child) {
    padding-right: 16px;
    width: 48px;
  }
  @media (max-width: 760px) {
    .enroll-toolbar {
      align-items: flex-start;
      flex-direction: column;
    }
    .enroll-head {
      align-items: flex-start;
      flex-direction: column;
      gap: 1px;
    }
    .enroll-actions {
      width: 100%;
      justify-content: space-between;
    }
  }
</style>
