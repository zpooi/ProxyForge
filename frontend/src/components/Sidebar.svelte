<script>
  import Icon from './Icon.svelte';

  export let activePath = '/';
  export let onNavigate;

  let mobileOpen = false;

  const navItems = [
    { href: '/', label: '仪表盘' },
    { href: '/accounts', label: '代理列表' },
    { href: '/nodes', label: '节点' },
    { href: '/settings', label: '代理设置' },
  ];

  function navigate(event, href) {
    mobileOpen = false;
    onNavigate(event, href);
  }

  function handleKeydown(event) {
    if (event.key === 'Escape') mobileOpen = false;
  }

  function handleResize() {
    if (window.innerWidth > 760) mobileOpen = false;
  }

  $: if (activePath) mobileOpen = false;
</script>

<svelte:window on:keydown={handleKeydown} on:resize={handleResize} />

<aside class="sidebar" class:mobile-open={mobileOpen}>
  <div class="sidebar-header">
    <div class="nav-brand">
      <span class="nav-brand-logo"><Icon name="bolt" size={18} /></span>
      <span class="nav-brand-text">ProxyForge</span>
    </div>
    <button
      type="button"
      class="mobile-menu-toggle"
      aria-label={mobileOpen ? '关闭菜单' : '打开菜单'}
      aria-expanded={mobileOpen}
      aria-controls="main-navigation"
      on:click={() => (mobileOpen = !mobileOpen)}
    >
      <Icon name={mobileOpen ? 'close' : 'menu'} size={22} />
    </button>
  </div>

  <div class="sidebar-panel" class:open={mobileOpen}>
    <nav id="main-navigation" class="sidebar-nav">
      {#each navItems as item}
        <a
          class:active={activePath === item.href}
          href={item.href}
          on:click={(event) => navigate(event, item.href)}
        >{item.label}</a>
      {/each}
    </nav>
    <div class="sidebar-footer">
      <a href="/logout">退出登录</a>
    </div>
  </div>
</aside>

{#if mobileOpen}
  <button type="button" class="nav-backdrop" aria-label="关闭菜单" on:click={() => (mobileOpen = false)}></button>
{/if}
