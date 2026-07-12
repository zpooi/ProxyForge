<script>
  import { onMount } from 'svelte';
  import StatCard from '../components/StatCard.svelte';
  import { fetchJSON } from '../lib/api.js';
  import { fmtBps, fmtBytes, fmtTime } from '../lib/format.js';
  import { chart } from '../lib/chart.js';

  let dashboard = null;
  let error = '';
  let egress = [];

  async function loadDashboard() {
    try {
      const data = await fetchJSON('/api/dashboard/json');
      dashboard = data;
      egress = Array.isArray(data.egress_ips) ? data.egress_ips : [];
      error = '';
    } catch (err) {
      error = err.message;
    }
  }

  onMount(() => {
    loadDashboard();
    const timer = setInterval(loadDashboard, 5000);
    return () => clearInterval(timer);
  });

  $: lastRun = dashboard ? fmtTime(dashboard.last_run_at, '从未') : '';
  $: nextRun = dashboard ? fmtTime(dashboard.next_run_at, '-') : '';
  $: clients = dashboard?.clients || [];
  $: activeEgress = egress.filter((e) => (e.current_up_bps || 0) + (e.current_down_bps || 0) > 0).length;

  $: egressOption = buildEgressOption(egress);

  const splitLine = { lineStyle: { color: '#f1f5f9' } };

  // 出口 IP 统计：按累计流量排序的出口 IP 上下行堆叠横向柱状图。
  function buildEgressOption(list) {
    const top = list.slice(0, 12).reverse();
    return {
      grid: { left: 12, right: 18, top: 28, bottom: 20, containLabel: true },
      tooltip: {
        trigger: 'axis',
        axisPointer: { type: 'shadow' },
        valueFormatter: (v) => fmtBytes(v),
      },
      legend: {
        data: ['下行', '上行'],
        right: 8,
        top: 0,
        icon: 'roundRect',
        itemWidth: 12,
        itemHeight: 12,
        textStyle: { color: '#64748b', fontSize: 12 },
      },
      xAxis: {
        type: 'value',
        axisLabel: { color: '#94a3b8', fontSize: 11, formatter: (v) => fmtBytes(v) },
        splitLine,
      },
      yAxis: {
        type: 'category',
        data: top.map((e) => e.ip),
        axisLine: { lineStyle: { color: '#e2e8f0' } },
        axisLabel: { color: '#64748b', fontSize: 11 },
      },
      series: [
        {
          name: '下行',
          type: 'bar',
          stack: 'traffic',
          data: top.map((e) => Number(e.total_down || 0)),
          itemStyle: { color: '#2563eb', borderRadius: [0, 4, 4, 0] },
          barMaxWidth: 18,
        },
        {
          name: '上行',
          type: 'bar',
          stack: 'traffic',
          data: top.map((e) => Number(e.total_up || 0)),
          itemStyle: { color: '#0ea5e9', borderRadius: [0, 4, 4, 0] },
          barMaxWidth: 18,
        },
      ],
    };
  }
</script>

<h2>仪表盘</h2>
{#if error}
  <div class="alert">加载失败：{error}</div>
{:else if dashboard}
  <div class="dashboard-grid">
    <StatCard
      label="可用出口 IP"
      value={dashboard.unique_ips || 0}
      sub={'代理槽位：' + (dashboard.proxy_slots || 0)}
    />
    <StatCard
      label="运行隧道"
      value={dashboard.running_tunnels || 0}
      sub={'目标池：' + (dashboard.target_pool || 0)}
    />
    <StatCard
      label="活跃出口 IP"
      value={activeEgress}
      sub={'共 ' + egress.length + ' 个出口'}
    />
    <StatCard
      label="累计流量"
      value={fmtBytes(dashboard.total_down || 0)}
      sub={'上行 ' + fmtBytes(dashboard.total_up || 0)}
    />
  </div>

  <div class="dashboard-columns">
    <section class="dashboard-section">
      <div class="section-header">
        <h3>出口 IP 流量</h3>
        <span class="section-sub">Top 12 · 按累计流量</span>
      </div>
      <div class="chart-box">
        {#if egress.length}
          <div class="echart" use:chart={egressOption}></div>
        {:else}
          <div class="chart-empty">暂无出口 IP 流量</div>
        {/if}
      </div>
    </section>

    <section class="dashboard-section">
      <div class="section-header">
        <h3>后台状态</h3>
        <span class="section-sub">{dashboard.running ? '运行中' : '空闲'}</span>
      </div>
      <div class="status-list">
        <div class="status-row"><span>代理端口</span><b>{dashboard.proxy_port || '-'}</b></div>
        <div class="status-row"><span>WARP 总数</span><b>{dashboard.total || 0}</b></div>
        <div class="status-row"><span>停用 / 错误</span><b>{dashboard.disabled || 0} / {dashboard.error || 0}</b></div>
        <div class="status-row"><span>上次检测</span><b>{lastRun}</b></div>
        <div class="status-row"><span>下次检测</span><b>{nextRun}</b></div>
      </div>
    </section>
  </div>

  <section class="dashboard-section">
    <div class="section-header">
      <h3>出口 IP 明细</h3>
      <span class="section-sub">按累计流量排序</span>
    </div>
    <div class="table-wrap compact">
      <table class="clients-table">
        <thead>
          <tr>
            <th>出口 IP</th>
            <th>地区 / 机房</th>
            <th>绑定账号</th>
            <th>延迟</th>
            <th>槽位</th>
            <th>实时 ↓/↑</th>
            <th>累计 ↓</th>
            <th>累计 ↑</th>
            <th>最近活跃</th>
          </tr>
        </thead>
        <tbody>
          {#if egress.length === 0}
            <tr>
              <td colspan="9">暂无出口 IP 记录</td>
            </tr>
          {:else}
            {#each egress as e}
              <tr>
                <td><code>{e.ip || '-'}</code></td>
                <td>{(e.country || '??') + (e.colo ? ' / ' + e.colo : '')}</td>
                <td>{e.account_tag || '-'}</td>
                <td>{e.latency_ms ? e.latency_ms + ' ms' : '-'}</td>
                <td>{e.slot_count || 0}</td>
                <td>{fmtBps(e.current_down_bps || 0)} / {fmtBps(e.current_up_bps || 0)}</td>
                <td>{fmtBytes(e.total_down || 0)}</td>
                <td>{fmtBytes(e.total_up || 0)}</td>
                <td>{fmtTime(e.last_seen_at, '-')}</td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
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
