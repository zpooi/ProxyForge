<script>
  import { onMount } from 'svelte';
  import StatCard from '../components/StatCard.svelte';
  import { fetchJSON } from '../lib/api.js';
  import { fmtBytes, fmtTime } from '../lib/format.js';

  let dashboard = null;
  let error = '';

  async function loadDashboard() {
    try {
      dashboard = await fetchJSON('/api/dashboard/json');
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
{:else}
  <div class="alert">正在加载仪表盘...</div>
{/if}
