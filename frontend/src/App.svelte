<script>
  import { onMount } from 'svelte';
  import Sidebar from './components/Sidebar.svelte';
  import Dashboard from './pages/Dashboard.svelte';
  import ProxyList from './pages/ProxyList.svelte';
  import Settings from './pages/Settings.svelte';
  import Nodes from './pages/Nodes.svelte';
  import Login from './pages/Login.svelte';
  import Setup from './pages/Setup.svelte';
  import Password from './pages/Password.svelte';

  let path = window.location.pathname;
  let search = window.location.search;

  function syncLocation() {
    path = window.location.pathname;
    search = window.location.search;
  }

  function navigate(event, href) {
    event.preventDefault();
    if (window.location.pathname !== href) {
      history.pushState({}, '', href);
      syncLocation();
    }
  }

  onMount(() => {
    window.addEventListener('popstate', syncLocation);
    return () => window.removeEventListener('popstate', syncLocation);
  });

  $: route = path === '/accounts' || path === '/settings' || path === '/settings/password' || path === '/nodes' || path === '/login' || path === '/setup' ? path : '/';
  $: authPage = route === '/login' || route === '/setup';
</script>

{#if authPage}
  {#if route === '/setup'}
    <Setup {search} />
  {:else}
    <Login {search} />
  {/if}
{:else}
  <div class="app-shell">
    <Sidebar activePath={route} onNavigate={navigate} />
    <main class="container">
      {#if route === '/accounts'}
        <ProxyList />
      {:else if route === '/settings'}
        <Settings />
      {:else if route === '/settings/password'}
        <Password {search} />
      {:else if route === '/nodes'}
        <Nodes />
      {:else}
        <Dashboard />
      {/if}
    </main>
  </div>
{/if}
