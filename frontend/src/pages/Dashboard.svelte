<script>
  import { onMount } from 'svelte';
  import StatCard from '../components/StatCard.svelte';
  import { fetchJSON } from '../lib/api.js';
  import { fmtBps, fmtBytes, fmtTime } from '../lib/format.js';

  let dashboard = null;
  let error = '';
  let samples = [];

  const chartWidth = 640;
  const chartHeight = 180;
  const chartPad = 18;
  const maxSamples = 36;

  async function loadDashboard() {
    try {
      const data = await fetchJSON('/api/dashboard/json');
      dashboard = data;
      appendTrafficSample(data);
      error = '';
    } catch (err) {
      error = err.message;
    }
  }

  function appendTrafficSample(data) {
    const at = new Date(data.now || Date.now()).getTime();
    if (!Number.isFinite(at)) return;

    const next = {
      at,
      totalUp: Number(data.total_up || 0),
      totalDown: Number(data.total_down || 0),
      upBps: 0,
      downBps: 0,
    };
    const prev = samples[samples.length - 1];
    if (prev && at <= prev.at) return;
    if (prev) {
      const seconds = Math.max(1, (at - prev.at) / 1000);
      next.upBps = Math.max(0, (next.totalUp - prev.totalUp) / seconds);
      next.downBps = Math.max(0, (next.totalDown - prev.totalDown) / seconds);
    }
    samples = [...samples, next].slice(-maxSamples);
  }

  function chartPath(key, maxValue) {
    if (samples.length < 2) return '';
    const usableWidth = chartWidth - chartPad * 2;
    const usableHeight = chartHeight - chartPad * 2;
    return samples
      .map((item, index) => {
        const x = chartPad + (usableWidth * index) / Math.max(1, samples.length - 1);
        const y = chartHeight - chartPad - (Math.min(item[key], maxValue) / maxValue) * usableHeight;
        return `${index === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${y.toFixed(1)}`;
      })
      .join(' ');
  }

  onMount(() => {
    loadDashboard();
    const timer = setInterval(loadDashboard, 5000);
    return () => clearInterval(timer);
  });

  $: lastRun = dashboard ? fmtTime(dashboard.last_run_at, '从未') : '';
  $: nextRun = dashboard ? fmtTime(dashboard.next_run_at, '-') : '';
  $: latestSample = samples[samples.length - 1] || { upBps: 0, downBps: 0 };
  $: maxBps = Math.max(1, ...samples.map((item) => Math.max(item.upBps, item.downBps)));
  $: upPath = chartPath('upBps', maxBps);
  $: downPath = chartPath('downBps', maxBps);
  $: clients = dashboard?.clients || [];
</script>

<h2>仪表盘</h2>
{#if error}
  <div class="alert">加载失败：{error}</div>
{:else if dashboard}
  <div class="dashboard-grid">
    <StatCard label="代理 IP 数" value={dashboard.proxy_slots || 0} />
    <StatCard label="可用 WARP" value={dashboard.active || 0} sub={'目标池：' + (dashboard.target_pool || 0)} />
    <StatCard label="唯一出口 IP" value={dashboard.unique_ips || 0} />
    <StatCard label="运行隧道" value={dashboard.running_tunnels || 0} />
    <StatCard label="代理端口" value={dashboard.proxy_port || '-'} />
    <StatCard label="WARP 总数" value={dashboard.total || 0} sub={'停用：' + (dashboard.disabled || 0) + ' / 错误：' + (dashboard.error || 0)} />
    <StatCard label="总上行" value={fmtBytes(dashboard.total_up || 0)} />
    <StatCard label="总下行" value={fmtBytes(dashboard.total_down || 0)} />
    <StatCard label="后台状态" value={dashboard.running ? '运行中' : '空闲'} sub={'上次：' + lastRun} />
    <StatCard label="下次自动检测" value={nextRun} />
  </div>

  <section class="dashboard-section">
    <div class="section-header">
      <h3>实时流量</h3>
      <div class="traffic-summary">
        <span><i class="dot up"></i>上行 {fmtBps(latestSample.upBps)}</span>
        <span><i class="dot down"></i>下行 {fmtBps(latestSample.downBps)}</span>
      </div>
    </div>
    <div class="traffic-chart">
      {#if samples.length > 1}
        <svg viewBox={`0 0 ${chartWidth} ${chartHeight}`} role="img" aria-label="实时流量统计图">
          <line x1={chartPad} y1={chartPad} x2={chartPad} y2={chartHeight - chartPad} />
          <line x1={chartPad} y1={chartHeight - chartPad} x2={chartWidth - chartPad} y2={chartHeight - chartPad} />
          <path class="area down" d={`${downPath} L ${chartWidth - chartPad} ${chartHeight - chartPad} L ${chartPad} ${chartHeight - chartPad} Z`} />
          <path class="line down" d={downPath} />
          <path class="line up" d={upPath} />
        </svg>
      {:else}
        <div class="chart-empty">等待下一次统计采样</div>
      {/if}
    </div>
  </section>

  <section class="dashboard-section">
    <div class="section-header">
      <h3>最近使用者 IP</h3>
      <span class="section-sub">按最近访问排序</span>
    </div>
    <div class="table-wrap compact">
      <table class="clients-table">
        <thead>
          <tr>
            <th>使用者 IP</th>
            <th>代理账号</th>
            <th>WARP 节点</th>
            <th>上行</th>
            <th>下行</th>
            <th>连接次数</th>
            <th>最近使用</th>
          </tr>
        </thead>
        <tbody>
          {#if clients.length === 0}
            <tr>
              <td colspan="7">暂时没有代理使用记录</td>
            </tr>
          {:else}
            {#each clients as client}
              <tr>
                <td><code>{client.client_ip || '-'}</code></td>
                <td>{client.username || '-'}</td>
                <td>{client.account_tag || '-'}</td>
                <td>{fmtBytes(client.total_up || 0)}</td>
                <td>{fmtBytes(client.total_down || 0)}</td>
                <td>{client.hit_count || 0}</td>
                <td>{fmtTime(client.last_seen_at, '-')}</td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  </section>
{:else}
  <div class="alert">正在加载仪表盘...</div>
{/if}
