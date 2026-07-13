<script>
  import { onMount, onDestroy } from 'svelte';
  import StatusTag from '../components/StatusTag.svelte';
  import Icon from '../components/Icon.svelte';

  let nodes = [];
  let loading = true;
  let error = '';
  let selectedAgentID = '';

  let installCommand = '';
  let uninstallCommand = '';
  let server = '';
  let hasBinary = true;
  let enrollError = '';
  let copied = '';
  let rotating = false;

  $: selectedAgent = selectedAgentID
    ? nodes.find((node) => node.kind === 'agent' && node.node_id === selectedAgentID) || null
    : null;

  async function fetchNodes() {
    try {
      const res = await fetch('/api/nodes/json');
      if (!res.ok) throw new Error('加载失败');
      const data = await res.json();
      nodes = data.nodes || [];
      if (selectedAgentID && !nodes.some((node) => node.node_id === selectedAgentID)) {
        selectedAgentID = '';
      }
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
    timer = setInterval(fetchNodes, 3000);
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

  function showAgent(node) {
    selectedAgentID = node.node_id;
  }

  function closeAgent() {
    selectedAgentID = '';
  }

  function displayInstallCommand(value) {
    if (!value) return '生成中…';
    return value.replace(/(Bearer\s+)([a-f0-9]+)/i, (_, prefix, token) => {
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
        <tr><th>节点</th><th>公网 IP</th><th>所在地</th><th>延迟</th><th>流量 ↑ / ↓</th><th>状态</th><th>操作</th></tr>
      </thead>
      <tbody>
        {#if nodes.length}
          {#each nodes as node}
            <tr>
              <td>
                <div class="node-name">{node.name}</div>
                <div class="node-kind">
                  {node.kind === 'local' ? '本机 WARP' : `Agent · ${node.egress_count || 0} 个 WARP 出口`}
                </div>
              </td>
              <td class="mono">{node.public_ip || '—'}</td>
              <td>{node.country || '—'}{node.colo ? ` / ${node.colo}` : ''}</td>
              <td>{node.latency_ms ? node.latency_ms + ' ms' : '—'}</td>
              <td class="mono">{fmtBytes(node.tx_bytes)} / {fmtBytes(node.rx_bytes)}</td>
              <td>
                <div class="node-status">
                  <StatusTag status={node.online ? 'active' : 'inactive'} />
                </div>
              </td>
              <td>
                {#if node.kind === 'agent'}
                  <button class="view-btn" title="查看 Agent 出口" on:click={() => showAgent(node)}>
                    查看
                    <Icon name="expand_more" size={16} />
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

{#if selectedAgent}
  <div class="modal-backdrop" role="presentation" on:click|self={closeAgent}>
    <section class="agent-modal" role="dialog" aria-modal="true" aria-label="Agent 出口详情">
      <div class="modal-header">
        <div>
          <h3>{selectedAgent.name}</h3>
          <p>{selectedAgent.public_ip || '公网 IP 未知'} · {selectedAgent.country || '地区未知'}{selectedAgent.colo ? ` / ${selectedAgent.colo}` : ''}</p>
        </div>
        <button class="modal-close" title="关闭" aria-label="关闭" on:click={closeAgent}>
          <Icon name="close" size={19} />
        </button>
      </div>
      <div class="egress-summary">
        <span><b>{selectedAgent.egress_count || 0}</b> 个 WARP 出口</span>
        <span>平均延迟 <b>{selectedAgent.latency_ms ? `${selectedAgent.latency_ms} ms` : '—'}</b></span>
        <span>流量 <b>{fmtBytes(selectedAgent.tx_bytes)} / {fmtBytes(selectedAgent.rx_bytes)}</b></span>
      </div>
      <div class="egress-table-wrap">
        <table class="egress-table">
          <thead>
            <tr><th>出口 IP</th><th>地区 / 机房</th><th>延迟</th><th>流量 ↑ / ↓</th></tr>
          </thead>
          <tbody>
            {#each selectedAgent.egresses || [] as egress}
              <tr>
                <td class="mono">{egress.public_ip || '—'}</td>
                <td>{egress.country || '—'}{egress.colo ? ` / ${egress.colo}` : ''}</td>
                <td>{egress.latency_ms ? `${egress.latency_ms} ms` : '—'}</td>
                <td class="mono">{fmtBytes(egress.tx_bytes)} / {fmtBytes(egress.rx_bytes)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
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
  .view-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 2px;
    min-height: 32px;
    padding: 5px 8px 5px 10px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--accent);
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
  }
  .view-btn:hover {
    border-color: var(--accent);
    background: var(--accent-soft, #eef2ff);
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
  .modal-backdrop {
    position: fixed;
    inset: 0;
    z-index: 80;
    display: grid;
    place-items: center;
    padding: 20px;
    background: rgba(15, 23, 42, 0.42);
  }
  .agent-modal {
    width: min(760px, 100%);
    max-height: min(680px, calc(100vh - 40px));
    overflow: hidden;
    background: var(--surface, #fff);
    border: 1px solid var(--border);
    border-radius: 10px;
    box-shadow: 0 20px 55px rgba(15, 23, 42, 0.2);
  }
  .modal-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    padding: 16px 18px 13px;
    border-bottom: 1px solid var(--border);
  }
  .modal-header h3 {
    margin: 0;
    font-size: 17px;
  }
  .modal-header p {
    margin: 4px 0 0;
    color: var(--text-3);
    font-size: 12px;
  }
  .modal-close {
    display: grid;
    place-items: center;
    width: 32px;
    min-width: 32px;
    height: 32px;
    padding: 0;
    background: transparent;
    color: var(--text-3);
  }
  .modal-close:hover {
    background: var(--surface-2, #f8fafc);
    color: var(--text);
  }
  .egress-summary {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 22px;
    padding: 11px 18px;
    background: var(--surface-2, #f8fafc);
    color: var(--text-3);
    font-size: 12px;
  }
  .egress-summary b {
    color: var(--text);
  }
  .egress-table-wrap {
    max-height: 430px;
    overflow: auto;
  }
  .egress-table {
    width: 100%;
    min-width: 620px;
    border-collapse: collapse;
  }
  .egress-table th,
  .egress-table td {
    padding: 11px 18px;
    border-bottom: 1px solid var(--border);
    text-align: left;
    font-size: 13px;
    white-space: nowrap;
  }
  .egress-table th {
    color: var(--text-3);
    font-size: 11px;
    font-weight: 600;
  }
  .egress-table tbody tr:last-child td {
    border-bottom: 0;
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
    width: 76px;
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
    .modal-backdrop {
      align-items: end;
      padding: 0;
    }
    .agent-modal {
      width: 100%;
      max-height: 78vh;
      border-width: 1px 0 0;
      border-radius: 10px 10px 0 0;
    }
    .modal-header,
    .egress-summary {
      padding-left: 14px;
      padding-right: 14px;
    }
  }
</style>
