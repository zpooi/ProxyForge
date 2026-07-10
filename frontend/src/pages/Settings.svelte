<script>
  import { onMount } from 'svelte';
  import { fetchJSON } from '../lib/api.js';

  const settingKeys = [
    'proxy_slot_count',
    'auto_generation',
    'proxy_port',
    'proxy_listen_addr',
    'proxy_public_host',
    'proxy_password',
    'proxy_tls',
    'dedup_interval_seconds',
  ];

  let settings = {};
  let error = '';
  let saved = new URLSearchParams(window.location.search).get('saved') === '1';
  let saving = false;

  async function loadSettings() {
    try {
      const data = await fetchJSON('/api/settings/json');
      settings = data.settings || {};
      error = '';
    } catch (err) {
      error = err.message;
    }
  }

  async function saveSettings() {
    saving = true;
    error = '';
    saved = false;
    try {
      const body = new URLSearchParams();
      for (const key of settingKeys) {
        body.set(key, settings[key] == null ? '' : String(settings[key]));
      }
      await fetchJSON('/api/settings', { method: 'POST', body });
      saved = true;
      await loadSettings();
    } catch (err) {
      error = err.message;
    } finally {
      saving = false;
    }
  }

  onMount(loadSettings);
</script>

<h2>代理设置</h2>
{#if saved}<div class="alert success">已保存</div>{/if}
{#if error}<div class="alert">保存失败：{error}</div>{/if}
<form class="settings-form" on:submit|preventDefault={saveSettings}>
  <fieldset>
    <legend>代理规模</legend>
    <label>代理 IP 数
      <input type="number" min="1" bind:value={settings.proxy_slot_count}>
    </label>
    <label>自动生成 WARP
      <select bind:value={settings.auto_generation}>
        <option value="on">开启</option>
        <option value="off">关闭</option>
      </select>
    </label>
    <p class="hint">设置需要长期使用的固定代理 IP 数。ProxyForge 会自动准备额外 WARP 候补，例如 20 个代理 IP 会自动维护更多候选节点。</p>
  </fieldset>
  <fieldset>
    <legend>代理监听</legend>
    <label>代理端口
      <input type="number" bind:value={settings.proxy_port}>
    </label>
    <label>监听地址
      <select bind:value={settings.proxy_listen_addr}>
        <option value="0.0.0.0">0.0.0.0</option>
        <option value="127.0.0.1">127.0.0.1</option>
      </select>
    </label>
    <label>全局密码
      <input type="text" bind:value={settings.proxy_password}>
    </label>
    <label>代理对外地址
      <input type="text" placeholder="留空则用当前访问域名" bind:value={settings.proxy_public_host}>
    </label>
    <p class="hint">导出订阅 / 复制代理链接时用的主机地址。填服务器真实 IP 或能直连代理端口的域名（如灰云子域名）。面板域名若经 Cloudflare / nginx 只反代了面板端口，请勿留空，否则客户端连不上代理端口。</p>
    <label>传输加密（TLS）
      <select bind:value={settings.proxy_tls}>
        <option value="on">开启（推荐）</option>
        <option value="off">关闭</option>
      </select>
    </label>
    <p class="hint">开启后，客户端↔代理这一跳套一层 TLS，把明文的 CONNECT 目标主机名藏进加密流，避开审查中间盒基于主机名的连接重置（表现为延迟能测出但访问被封域名时连接被重置）。同一端口同时兼容明文与 TLS，导出的 Clash 订阅会自动带上 tls 配置，无需额外部署。</p>
  </fieldset>
  <fieldset>
    <legend>自动检测</legend>
    <label>检测和修复间隔（秒）
      <input type="number" bind:value={settings.dedup_interval_seconds}>
    </label>
    <p class="hint">节点生成、测试、去重、失败重绑由后台自动处理，通常只需要调整代理 IP 数。</p>
  </fieldset>
  <div class="form-actions">
    <button type="submit" disabled={saving}>{saving ? '保存中...' : '保存设置'}</button>
    <a class="secondary-link" href="/settings/password">修改后台账号</a>
  </div>
</form>
