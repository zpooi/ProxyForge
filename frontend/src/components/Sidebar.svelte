<script>
  import Icon from './Icon.svelte';

  export let activePath = '/';
  export let onNavigate;

  const navItems = [
    { href: '/', label: '仪表盘' },
    { href: '/accounts', label: '代理列表' },
    { href: '/nodes', label: '节点' },
    { href: '/settings', label: '代理设置' },
  ];
</script>

<aside class="sidebar">
  <div class="nav-brand"><span class="nav-brand-logo"><Icon name="bolt" size={18} /></span>ProxyForge</div>
  <nav class="sidebar-nav">
    {#each navItems as item}
      <a
        class:active={activePath === item.href}
        href={item.href}
        on:click={(event) => onNavigate(event, item.href)}
      >{item.label}</a>
    {/each}
  </nav>
  <div class="sidebar-footer">
    <a href="/logout">退出登录</a>
  </div>
</aside>
