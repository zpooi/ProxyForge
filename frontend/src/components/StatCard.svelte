<script>
  export let label;
  export let value;
  export let sub = '';
</script>

<div class="stat-card">
  <div class="label">{label}</div>
  <div class="value">{value}</div>
  {#if sub}<div class="sub">{sub}</div>{/if}
</div>
