<script>
  export let search = '';

  $: error = new URLSearchParams(search).get('error') || '';
</script>

<div class="login-wrapper">
  <form class="login-card" method="post" action="/login">
    <h1>ProxyForge</h1>
    <p class="hint">请输入初始化时设置的后台账号和密码</p>
    {#if error}<div class="alert">{error}</div>{/if}
    <label>用户名
      <input type="text" name="username" required autofocus>
    </label>
    <label>密码
      <input type="password" name="password" required>
    </label>
    <button type="submit">登录</button>
  </form>
</div>
