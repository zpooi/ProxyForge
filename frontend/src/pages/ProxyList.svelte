<script>
  import { onMount } from 'svelte';
  import StatusTag from '../components/StatusTag.svelte';
  import { fetchJSON } from '../lib/api.js';
  import { fmtBps, metric } from '../lib/format.js';
  import { slotState } from '../lib/status.js';

  let slots = [];
  let proxyHost = window.location.hostname;
  let proxyPort = 7843;
  let error = '';
  let copied = '';

  async function loadAccounts() {
    try {
      const data = await fetchJSON('/api/accounts/json');
      slots = data.slots || [];
      proxyHost = data.proxy_host || window.location.hostname;
      proxyPort = data.proxy_port || 7843;
      error = '';
    } catch (err) {
      error = err.message;
    }
  }

  onMount(() => {
    loadAccounts();
    const timer = setInterval(loadAccounts, 5000);
    return () => clearInterval(timer);
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

  async function copyProxy(slot, scheme) {
    const text = proxyAddress(slot, scheme);
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
      } else {
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
      copied = `${slot.username}-${scheme}`;
      setTimeout(() => {
        if (copied === `${slot.username}-${scheme}`) copied = '';
      }, 1600);
    } catch (err) {
      error = '复制失败：' + err.message;
    }
  }
</script>

<h2>代理列表</h2>
<div class="page-actions">
  <a class="export-link" href="/api/export?format=plain" target="_blank" rel="noreferrer">导出代理列表</a>
  <a class="export-link" href="/api/export?format=clash" target="_blank" rel="noreferrer">导出 Clash 配置</a>
</div>
{#if error}
  <div class="alert">加载失败：{error}</div>
{/if}
<div class="table-wrap">
  <table>
    <thead>
      <tr>
        <th>账号</th><th>密码</th><th>稳定出口 IP</th><th>国家</th>
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
            <td><StatusTag status={slotState(slot)} /></td>
            <td>
              <div class="copy-actions">
                <button type="button" class="copy-button" on:click={() => copyProxy(slot, 'http')}>HTTP</button>
                <button type="button" class="copy-button secondary" on:click={() => copyProxy(slot, 'socks5')}>SOCKS5</button>
                {#if copied === `${slot.username}-http` || copied === `${slot.username}-socks5`}
                  <span class="copy-feedback">已复制</span>
                {/if}
              </div>
            </td>
          </tr>
        {/each}
      {:else}
        <tr><td colspan="9">暂无代理账号，后台会根据设置自动创建</td></tr>
      {/if}
    </tbody>
  </table>
</div>
