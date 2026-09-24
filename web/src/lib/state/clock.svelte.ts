/**
 * 页面时钟：让"3 分钟前""剩余 12 分"这类相对时间周期性重算。
 * 在组件初始化期间调用（内部使用 $effect）。
 */
export function createClock(intervalMs = 30_000) {
  let now = $state(new Date());
  $effect(() => {
    const timer = setInterval(() => {
      now = new Date();
    }, intervalMs);
    return () => clearInterval(timer);
  });
  return {
    get now(): Date {
      return now;
    },
  };
}
