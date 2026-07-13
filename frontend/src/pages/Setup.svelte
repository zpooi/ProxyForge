<script>
  export let search = '';

  $: error = new URLSearchParams(search).get('error') || '';
</script>

<div class="login-wrapper">
  <form class="login-card" method="post" action="/setup">
    <h1>ProxyForge</h1>
    <p class="hint">首次使用需要初始化后台账号。数据库、默认设置和代理槽位会自动创建。</p>
    {#if error}<div class="alert">{error}</div>{/if}
    <label>用户名
      <input type="text" name="username" required minlength="3" autofocus>
    </label>
    <label>密码
      <input type="password" name="password" required minlength="12" autocomplete="new-password">
    </label>
    <label>确认密码
      <input type="password" name="confirm_password" required minlength="12" autocomplete="new-password">
    </label>
    <button type="submit">初始化并登录</button>
  </form>
</div>
