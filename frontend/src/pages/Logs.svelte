<script>
  import { onMount, tick } from 'svelte';
  import { fetchJSON } from '../lib/api.js';

  let content = '';
  let date = '';
  let offset = 0;
  let error = '';
  let loading = true;
  let refreshing = false;
  let followTail = true;
  let logView;
  let stopped = false;

  async function refresh() {
    if (refreshing || stopped) return;
    refreshing = true;
    try {
      // Bound each refresh to 4 MiB so a very busy day cannot freeze the UI.
      // Further chunks are picked up by the next two-second refresh.
      for (let i = 0; i < 16; i += 1) {
        const params = new URLSearchParams({ offset: String(offset) });
        if (date) params.set('date', date);
        const data = await fetchJSON(`/api/logs/live?${params}`);
        if (stopped) return;
        if (data.date !== date) {
          date = data.date || '';
          content = '';
        }
        content += data.content || '';
        offset = Number(data.next) || 0;
        if (!data.more) break;
      }
      error = '';
      if (followTail) {
        await tick();
        if (logView) logView.scrollTop = logView.scrollHeight;
      }
    } catch (err) {
      error = err.message;
    } finally {
      loading = false;
      refreshing = false;
    }
  }

  function handleScroll() {
    if (!logView) return;
    followTail = logView.scrollHeight - logView.scrollTop - logView.clientHeight < 40;
  }

  function jumpToLatest() {
    followTail = true;
    if (logView) logView.scrollTop = logView.scrollHeight;
  }

  onMount(() => {
    refresh();
    const timer = window.setInterval(refresh, 2000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  });
</script>

<div class="logs-heading">
  <div>
    <h2>实时日志</h2>
    <p>仅保留当天的程序日志，每 2 秒自动更新。</p>
  </div>
  <div class="page-actions">
    {#if !followTail}
      <button type="button" class="secondary" on:click={jumpToLatest}>回到最新</button>
    {/if}
    <button type="button" class="secondary" disabled={refreshing} on:click={refresh}>
      {refreshing ? '刷新中…' : '立即刷新'}
    </button>
    <a class="export-link" href="/api/logs/download">下载当日日志</a>
  </div>
</div>

{#if error}<div class="alert">日志读取失败：{error}</div>{/if}

<section class="log-panel">
  <div class="log-toolbar">
    <span class="live-dot"></span>
    <strong>{date || '今天'}</strong>
    <span>{loading ? '正在载入…' : '实时更新中'}</span>
  </div>
  <pre class="log-output" bind:this={logView} on:scroll={handleScroll}>{content || (loading ? '正在读取日志…' : '当天暂无日志')}</pre>
</section>

<style>
  .logs-heading {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 18px;
  }

  .logs-heading h2 { margin-bottom: 5px; }

  .logs-heading p {
    margin: 0;
    color: var(--text-3);
    font-size: 13px;
  }

  .log-panel {
    overflow: hidden;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: #111827;
    box-shadow: var(--shadow-sm);
  }

  .log-toolbar {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 44px;
    padding: 0 16px;
    border-bottom: 1px solid #283449;
    color: #93a4bd;
    background: #172033;
    font-size: 12px;
  }

  .log-toolbar strong { color: #e5edf8; }

  .live-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #34d399;
    box-shadow: 0 0 0 3px rgba(52, 211, 153, 0.13);
  }

  .log-output {
    width: 100%;
    height: calc(100vh - 230px);
    min-height: 420px;
    margin: 0;
    overflow: auto;
    padding: 16px;
    color: #d6deeb;
    background: #111827;
    font: 12px/1.65 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    tab-size: 2;
  }

  @media (max-width: 760px) {
    .logs-heading { flex-direction: column; }
    .logs-heading .page-actions { width: 100%; }
    .logs-heading .page-actions > * { flex: 1 1 auto; }
    .log-output {
      height: calc(100dvh - 290px);
      min-height: 360px;
      padding: 13px;
      font-size: 11px;
    }
  }
</style>
