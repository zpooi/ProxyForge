// echarts 通过 UMD 挂在 window.echarts（见 build.mjs 内嵌的 echarts.min.js）。
// chart(node, option) 是一个 Svelte action：挂载时初始化图表，option 变化时
// 增量更新，窗口尺寸变化时自适应，销毁时清理。
export function chart(node, option) {
  const echarts = window.echarts;
  if (!echarts) {
    node.textContent = '图表库未加载';
    return {};
  }

  const instance = echarts.init(node, null, { renderer: 'canvas' });
  if (option) instance.setOption(option);

  const onResize = () => instance.resize();
  window.addEventListener('resize', onResize);

  // 侧边栏折叠等布局变化时容器尺寸会变，用 ResizeObserver 兜底。
  let ro;
  if (typeof ResizeObserver !== 'undefined') {
    ro = new ResizeObserver(() => instance.resize());
    ro.observe(node);
  }

  return {
    update(next) {
      if (next) instance.setOption(next, { notMerge: false });
    },
    destroy() {
      window.removeEventListener('resize', onResize);
      if (ro) ro.disconnect();
      instance.dispose();
    },
  };
}
