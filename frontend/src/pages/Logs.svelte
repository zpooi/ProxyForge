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

  // 把标准 log 行拆成「时间 + 正文」，展示成类似 GRA 的中文实时日志。
  $: logLines = formatLogLines(content);

  function formatLogLines(raw) {
    if (!raw) return [];
    return String(raw).split(/\r?\n/).filter((line) => line.length > 0).map((line) => {
      // 常见：15:04:05 message  或  2006/01/02 15:04:05 message
      let m = line.match(/^(\d{2}:\d{2}:\d{2})\s+(.*)$/);
      if (m) return { time: m[1], text: m[2] };
      m = line.match(/^\d{4}\/\d{2}\/\d{2}\s+(\d{2}:\d{2}:\d{2})\s+(.*)$/);
      if (m) return { time: m[1], text: m[2] };
      m = line.match(/^\d{4}-\d{2}-\d{2}\s+(\d{2}:\d{2}:\d{2})[,\s]+(.*)$/);
      if (m) return { time: m[1], text: m[2] };
      return { time: '', text: line };
    });
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
    <p>中文实时日志：谁在连接、用哪个账号/出口、是否还在用。每 2 秒自动更新，仅保留当天。</p>
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
    <span>{loading ? '正在载入…' : '实时更新中 · 含连接开始/结束与活跃汇总'}</span>
  </div>
  <div class="log-output" bind:this={logView} on:scroll={handleScroll}>
    {#if loading && !content}
      <div class="log-empty">正在读取日志…</div>
    {:else if logLines.length === 0}
      <div class="log-empty">当天暂无日志</div>
    {:else}
      {#each logLines as row}
        <div class="log-line">
          {#if row.time}<span class="log-time">{row.time}</span>{/if}
          <span class="log-text">{row.text}</span>
        </div>
      {/each}
    {/if}
  </div>
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
    padding: 12px 16px;
    color: #d6deeb;
    background: #111827;
    font: 12.5px/1.7 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  }
  .log-line {
    display: grid;
    grid-template-columns: 72px minmax(0, 1fr);
    gap: 10px;
    padding: 2px 0;
  }
  .log-time {
    color: #7dd3fc;
    font-variant-numeric: tabular-nums;
    user-select: none;
  }
  .log-text {
    color: #e5edf8;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }
  .log-empty {
    color: #93a4bd;
    padding: 8px 0;
  }

  @media (max-width: 760px) {
    .logs-heading { flex-direction: column; }
    .logs-heading .page-actions { width: 100%; }
    .logs-heading .page-actions > * { flex: 1 1 auto; }
    .log-output {
      height: calc(100dvh - 290px);
      min-height: 360px;
      padding: 12px;
      font-size: 11px;
    }
    .log-line {
      grid-template-columns: 58px minmax(0, 1fr);
      gap: 8px;
    }
  }
</style>
