<script>
  export let search = '';

  $: error = new URLSearchParams(search).get('error') || '';
</script>

<h2>修改后台账号</h2>
{#if error}<div class="alert">{error}</div>{/if}
<form method="post" action="/settings/password" class="settings-form">
  <label>新用户名（留空不变）
    <input type="text" name="username" minlength="3">
  </label>
  <label>旧密码
    <input type="password" name="old_password" required>
  </label>
  <label>新密码（至少 12 位）
    <input type="password" name="new_password" required minlength="12" autocomplete="new-password">
  </label>
  <label>确认新密码
    <input type="password" name="confirm_password" required minlength="12" autocomplete="new-password">
  </label>
  <button type="submit">保存账号</button>
</form>
