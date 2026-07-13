<script>
  import { onMount } from 'svelte';
  import StatusTag from '../components/StatusTag.svelte';
  import Icon from '../components/Icon.svelte';
  import { fetchJSON } from '../lib/api.js';
  import { fmtBps, metric, prettyError } from '../lib/format.js';
  import { slotState } from '../lib/status.js';

  const PLAIN_SCHEMES = [
    { id: 'http', label: 'HTTP' },
    { id: 'socks5', label: 'SOCKS5' },
  ];
  const TLS_SCHEMES = [{ id: 'https', label: 'HTTPS' }];

  let slots = [];
  // 在线远程 agent 的 WARP 出口，和本机槽位共用代理端口，用户名 node-<id>。
  let agents = [];
  let proxyHost = window.location.hostname;
  let proxyPort = 7843;
  let proxyTLS = true;
  let error = '';
  let copied = '';
  let subUrl = '';
  // 统一轮换凭据：一条连接串，服务端在所有节点（本机 + 各地区 agent）间自动粘滞轮换。
  let rotate = null;
  // 打开中的复制菜单，用 fixed 定位避开表格 overflow 裁剪。
  let menu = null;

  async function loadAccounts() {
    try {
      const data = await fetchJSON('/api/accounts/json');
      slots = data.slots || [];
      agents = data.agents || [];
      proxyHost = data.proxy_host || window.location.hostname;
      proxyPort = data.proxy_port || 7843;
      proxyTLS = data.proxy_tls !== false;
      error = '';
    } catch (err) {
      error = err.message;
    }
  }

  async function loadSubscription() {
    try {
      const data = await fetchJSON('/api/subscription');
      if (data.token) {
        subUrl = `${window.location.origin}${data.path}?token=${data.token}`;
      }
    } catch (err) {
      error = err.message;
    }
  }

  // 拉取统一轮换凭据的连接信息。非关键路径，失败不打断页面。
  async function loadRotate() {
    try {
      rotate = await fetchJSON('/api/nodes/rotate');
    } catch (err) {
      // 忽略：没有节点或未配置时该卡片不显示
    }
  }

  function toggleMenu(event, slot) {
    if (menu && menu.kind === 'slot' && menu.username === slot.username) {
      menu = null;
      return;
    }
    const rect = event.currentTarget.getBoundingClientRect();
    // 右对齐到按钮右边缘，用 right 偏移避免菜单往右溢出视口。
    menu = { kind: 'slot', username: slot.username, right: window.innerWidth - rect.right, y: rect.bottom + 4 };
  }

  function toggleRotateMenu(event) {
    if (menu && menu.kind === 'rotate') {
      menu = null;
      return;
    }
    const rect = event.currentTarget.getBoundingClientRect();
    menu = { kind: 'rotate', right: window.innerWidth - rect.right, y: rect.bottom + 4 };
  }

  function closeMenu() {
    menu = null;
  }

  onMount(() => {
    loadAccounts();
    loadSubscription();
    loadRotate();
    const timer = setInterval(loadAccounts, 5000);
    // 菜单靠 fixed 定位，滚动或改变尺寸后位置会失效，直接关掉。
    window.addEventListener('scroll', closeMenu, true);
    window.addEventListener('resize', closeMenu);
    return () => {
      clearInterval(timer);
      window.removeEventListener('scroll', closeMenu, true);
      window.removeEventListener('resize', closeMenu);
    };
  });

  function countryLabel(code) {
    return code ? String(code).toUpperCase() : '-';
  }

  function proxyAddress(slot, scheme) {
    const user = encodeURIComponent(slot.username || '');
    const pass = encodeURIComponent(slot.password || '');
    const host = formatProxyHost(proxyHost);
    return `${scheme}://${user}:${pass}@${host}:${proxyPort}`;
  }

  function formatProxyHost(host) {
    const value = String(host || window.location.hostname);
    if (value.includes(':') && !value.startsWith('[')) return `[${value}]`;
    return value;
  }

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

  async function copyProxy(slot, scheme) {
    const text = proxyAddress(slot, scheme);
    try {
      await writeClipboard(text);
      menu = null;
      copied = `${slot.username}-${scheme}`;
      setTimeout(() => {
        if (copied === `${slot.username}-${scheme}`) copied = '';
      }, 1600);
    } catch (err) {
      error = '复制失败：' + err.message;
    }
  }

  async function copySubscription() {
    if (!subUrl) return;
    try {
      await writeClipboard(subUrl);
      copied = 'subscription';
      setTimeout(() => {
        if (copied === 'subscription') copied = '';
      }, 1600);
    } catch (err) {
      error = '复制失败：' + err.message;
    }
  }

  function displaySubscriptionURL(value) {
    if (!value) return '生成中…';
    return value.replace(/(token=)([^&\s]+)/, (_, prefix, token) => {
      if (token.length <= 16) return prefix + token;
      return `${prefix}${token.slice(0, 8)}…${token.slice(-4)}`;
    });
  }

  // 统一轮换链接的地址：与单个代理同格式（scheme://user:pass@host:port），
  // 用户名固定为 rotate.username（auto），服务端据此在所有节点间轮换出口。
  function rotateAddress(scheme) {
    if (!rotate) return '';
    const user = encodeURIComponent(rotate.username || 'auto');
    const pass = encodeURIComponent(rotate.password || '');
    const host = formatProxyHost(rotate.host || proxyHost);
    const port = rotate.port || proxyPort;
    const cred = pass ? `${user}:${pass}` : user;
    return `${scheme}://${cred}@${host}:${port}`;
  }

  async function copyRotate(scheme) {
    const text = rotateAddress(scheme);
    if (!text) return;
    try {
      await writeClipboard(text);
      menu = null;
      copied = `rotate-${scheme}`;
      setTimeout(() => {
        if (copied === `rotate-${scheme}`) copied = '';
      }, 1600);
    } catch (err) {
      error = '复制失败：' + err.message;
    }
  }
</script>

<h2>代理列表</h2>
<div class="sub-bar">
  <div class="subscription-row">
    <code class="subscription-url" title={subUrl}>{displaySubscriptionURL(subUrl)}</code>
    <button
      type="button"
      class="copy-trigger subscription-copy"
      class:done={copied === 'subscription'}
      title={copied === 'subscription' ? '已复制' : '复制 Clash 订阅'}
      aria-label={copied === 'subscription' ? '已复制' : '复制 Clash 订阅'}
      on:click={copySubscription}
      disabled={!subUrl}
    >
      <Icon name={copied === 'subscription' ? 'check' : 'copy'} size={18} />
    </button>
  </div>
  {#if subUrl}
    <p class="sub-hint">Clash / Mihomo 添加为订阅；链接含 token，请勿外泄。</p>
  {/if}
  {#if rotate && rotate.host}
    <div class="rotate-block">
      <div class="rotate-summary">
        <strong>统一轮换</strong><span>3 分钟粘滞 · 故障切换</span>
      </div>
      <button
        type="button"
        class="copy-trigger rotate-trigger"
        class:done={copied === 'rotate-http' || copied === 'rotate-socks5'}
        class:open={menu && menu.kind === 'rotate'}
        title="复制轮换链接"
        aria-label="复制轮换链接"
        on:click={toggleRotateMenu}
      >
        <Icon name={copied === 'rotate-http' || copied === 'rotate-socks5' ? 'check' : 'copy'} size={18} />
        <Icon name="expand_more" size={16} />
      </button>
    </div>
  {/if}
</div>
{#if error}
  <div class="alert">加载失败：{error}</div>
{/if}

<style>
  .subscription-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 40px;
    align-items: center;
    gap: 8px;
  }
  .subscription-url {
    display: block;
    min-width: 0;
    padding: 10px 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    background: #0f172a;
    color: #e2e8f0;
    border-radius: 8px;
    font-size: 13px;
  }
  .subscription-copy {
    width: 40px;
    min-height: 40px;
    padding: 8px;
  }
  .rotate-block {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    gap: 16px;
    margin-top: 6px;
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    box-shadow: var(--shadow-sm);
  }
  .rotate-summary {
    display: flex;
    align-items: baseline;
    gap: 10px;
    min-width: 0;
  }
  .rotate-summary strong {
    color: var(--text);
    font-size: 14px;
  }
  .rotate-summary span {
    color: var(--text-3);
    font-size: 12px;
    white-space: nowrap;
  }
  .rotate-trigger {
    min-width: 42px;
    min-height: 34px;
    margin-left: auto;
    flex-shrink: 0;
  }
  @media (max-width: 620px) {
    .rotate-summary {
      align-items: flex-start;
      flex-direction: column;
      gap: 1px;
    }
  }
  .row-sub {
    display: block;
    margin-top: 2px;
    color: var(--text-3);
    font-size: 12px;
  }
  /* 账号列限宽，让密码列整体左移、更靠近账号；密码不换行避免撑高行。 */
  .name-col {
    width: 160px;
  }
  .pw-col {
    width: 200px;
  }
</style>
<div class="table-wrap">
  <table>
    <thead>
      <tr>
        <th class="name-col">账号</th><th class="pw-col">密码</th><th>稳定出口 IP</th><th>国家</th>
        <th>延迟</th><th>速度</th><th>丢包</th><th>状态</th><th>复制代理</th>
      </tr>
    </thead>
    <tbody>
      {#if slots.length}
        {#each slots as slot}
          <tr>
            <td>{slot.username}</td>
            <td><code>{slot.password}</code></td>
            <td>{slot.pinned_public_ip || slot.public_ip || (slot.account_tag ? '检测中' : '等待可用节点')}</td>
            <td>{countryLabel(slot.country)}</td>
            <td>{slot.account_tag ? metric(slot.latency_ms, ' ms') : '-'}</td>
            <td>{slot.speed_bps ? fmtBps(slot.speed_bps) : '-'}</td>
            <td>{slot.packet_loss ? (slot.packet_loss * 100).toFixed(0) + '%' : '0%'}</td>
            <td title={prettyError(slot.last_error)}><StatusTag status={slotState(slot)} /></td>
            <td>
              <button
                type="button"
                class="icon-button copy-trigger"
                class:done={copied === `${slot.username}-http` || copied === `${slot.username}-socks5`}
                class:open={menu && menu.kind === 'slot' && menu.username === slot.username}
                title="复制代理链接"
                aria-label="复制代理链接"
                on:click={(e) => toggleMenu(e, slot)}
              >
                <Icon name={copied === `${slot.username}-http` || copied === `${slot.username}-socks5` ? 'check' : 'copy'} size={18} />
                <Icon name="expand_more" size={16} />
              </button>
            </td>
          </tr>
        {/each}
      {/if}
      {#each agents as agent}
        <tr>
          <td>
            {agent.name}
            <span class="row-sub">Agent WARP 出口 · {agent.agent_name}</span>
          </td>
          <td><code>{agent.password}</code></td>
          <td>{agent.public_ip || '检测中'}</td>
          <td>{countryLabel(agent.country)}</td>
          <td>{agent.latency_ms ? metric(agent.latency_ms, ' ms') : '-'}</td>
          <td>{agent.speed_bps ? fmtBps(agent.speed_bps) : '0 B/s'}</td>
          <td>0%</td>
          <td><StatusTag status="ok" /></td>
          <td>
            <button
              type="button"
              class="icon-button copy-trigger"
              class:done={copied === `${agent.username}-http` || copied === `${agent.username}-socks5`}
              class:open={menu && menu.kind === 'slot' && menu.username === agent.username}
              title="复制代理链接"
              aria-label="复制代理链接"
              on:click={(e) => toggleMenu(e, agent)}
            >
              <Icon name={copied === `${agent.username}-http` || copied === `${agent.username}-socks5` ? 'check' : 'copy'} size={18} />
              <Icon name="expand_more" size={16} />
            </button>
          </td>
        </tr>
      {/each}
      {#if !slots.length && !agents.length}
        <tr><td colspan="9">暂无代理账号，后台会根据设置自动创建</td></tr>
      {/if}
    </tbody>
  </table>
</div>

{#if menu}
  <div class="menu-backdrop" on:click={closeMenu} on:contextmenu|preventDefault={closeMenu}></div>
  <div class="copy-menu" style="right: {menu.right}px; top: {menu.y}px;">
    <div class="copy-menu-title">复制为</div>
    {#if menu.kind === 'rotate'}
      {#each (proxyTLS ? TLS_SCHEMES : PLAIN_SCHEMES) as scheme}
        <button type="button" class="copy-menu-item" on:click={() => copyRotate(scheme.id)}>
          <Icon name="copy" size={16} />
          <span>{scheme.label}</span>
        </button>
      {/each}
    {:else}
      {@const slot = slots.find((s) => s.username === menu.username) || agents.find((a) => a.username === menu.username)}
      {#each (proxyTLS ? TLS_SCHEMES : PLAIN_SCHEMES) as scheme}
        <button type="button" class="copy-menu-item" on:click={() => copyProxy(slot, scheme.id)}>
          <Icon name="copy" size={16} />
          <span>{scheme.label}</span>
        </button>
      {/each}
    {/if}
  </div>
{/if}
