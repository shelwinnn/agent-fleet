<!--
  测试挂载件：验证"切换语言后当前页面立即更新"（不重挂载、不刷新）。
-->
<script lang="ts">
  import { t, i18n, setLocale } from '../src/lib/i18n/index.js';
  import { formatRelativeTime } from '../src/lib/state/format.js';
</script>

<p data-testid="nav">{t('nav.machines')}</p>
<p data-testid="time">{formatRelativeTime(new Date(Date.now() - 3 * 60_000), new Date(), i18n.locale)}</p>
<button data-testid="switch" onclick={() => setLocale('en')}>switch</button>
